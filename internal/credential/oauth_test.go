package credential

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeOAuthStore is an in-memory OAuthAccessStore for resolver tests.
type fakeOAuthStore struct {
	byPublicID map[string]OAuthAccessRecord
	touched    []string
}

func (f *fakeOAuthStore) CreateOAuthAccess(_ context.Context, rec OAuthAccessRecord) (OAuthAccessRecord, error) {
	if f.byPublicID == nil {
		f.byPublicID = map[string]OAuthAccessRecord{}
	}
	rec.ID = "id-" + rec.PublicID
	f.byPublicID[rec.PublicID] = rec
	return rec, nil
}

func (f *fakeOAuthStore) GetOAuthAccessByPublicID(_ context.Context, publicID string) (OAuthAccessRecord, error) {
	rec, ok := f.byPublicID[publicID]
	if !ok {
		return OAuthAccessRecord{}, errors.New("not found")
	}
	return rec, nil
}

func (f *fakeOAuthStore) TouchOAuthAccessLastUsed(_ context.Context, id string) (int64, error) {
	f.touched = append(f.touched, id)
	return 1, nil
}

// errScoped rejects scoped tokens -- used to prove a JWT bearer is never accepted
// via any full-access path (it is treated as an unknown opaque token and denied).
type errScoped struct{}

func (errScoped) AuthenticateScoped(context.Context, string) (ScopedResult, error) {
	return ScopedResult{}, errors.New("not scoped")
}

func newOAuthTestService() (*Service, *fakeOAuthStore) {
	pat := &fakeStore{
		byPublicID: map[string]PATRecord{},
		users:      map[string]Identity{"u1": {UserID: "u1", Email: "u1@x", IsActive: true, Role: "user"}},
	}
	oa := &fakeOAuthStore{byPublicID: map[string]OAuthAccessRecord{}}
	svc := NewService(Config{PATs: pat, OAuth: oa, Users: pat, Tokens: errScoped{}})
	return svc, oa
}

func TestResolveOAuthAccessRoundTrip(t *testing.T) {
	svc, store := newOAuthTestService()
	ctx := context.Background()

	plaintext, err := svc.IssueOAuthAccess(ctx, "u1", "client-1", []string{"tasks:read"}, "fam-1", time.Hour)
	if err != nil {
		t.Fatalf("issue oauth access: %v", err)
	}
	publicID, _, err := ParseOpaqueToken(OAuthAccessPrefix, plaintext)
	if err != nil {
		t.Fatalf("parse issued token: %v", err)
	}
	if rec, ok := store.byPublicID[publicID]; !ok || rec.TokenHash == "" {
		t.Fatal("issued record missing or unhashed")
	}

	p, err := svc.Resolve(ctx, "Bearer "+plaintext)
	if err != nil {
		t.Fatalf("resolve valid oauth token: %v", err)
	}
	if p == nil || p.Kind != KindOAuth || p.UserID != "u1" {
		t.Fatalf("unexpected principal: %+v", p)
	}
	if p.IsAdmin {
		t.Fatal("oauth tokens must never carry admin")
	}
	if got := MatchScope(p.Scopes, "tasks:read"); !got {
		t.Fatal("resolved principal missing granted scope")
	}
}

func TestResolveOAuthRevokedAndExpired(t *testing.T) {
	svc, store := newOAuthTestService()
	ctx := context.Background()

	// Family revoked -> the access token is dead at read time.
	plaintext, err := svc.IssueOAuthAccess(ctx, "u1", "c", []string{"tasks:read"}, "f", time.Hour)
	if err != nil {
		t.Fatalf("issue oauth access: %v", err)
	}
	publicID, _, _ := ParseOpaqueToken(OAuthAccessPrefix, plaintext)
	now := time.Now()
	r := store.byPublicID[publicID]
	r.FamilyRevokedAt = &now
	store.byPublicID[publicID] = r
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext); err == nil {
		t.Fatal("revoked-family oauth token must be denied")
	}

	// Expired.
	plaintext2, err := svc.IssueOAuthAccess(ctx, "u1", "c", []string{"tasks:read"}, "f", time.Hour)
	if err != nil {
		t.Fatalf("issue oauth access: %v", err)
	}
	publicID2, _, _ := ParseOpaqueToken(OAuthAccessPrefix, plaintext2)
	r2 := store.byPublicID[publicID2]
	r2.ExpiresAt = time.Now().Add(-time.Minute)
	store.byPublicID[publicID2] = r2
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext2); err == nil {
		t.Fatal("expired oauth token must be denied")
	}
}

// The non-negotiable guardrail: a JWT bearer is NEVER JWKS-validated at the API
// boundary. It has no stella_ prefix, so it falls to the opaque legacy lookup and
// is denied. Only opaque stella_oat_ tokens resolve to an oauth principal.
func TestResolveJWTBearerRejected(t *testing.T) {
	svc, _ := newOAuthTestService()
	jwt := "Bearer eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0." +
		"eyJzdWIiOiJ1MSIsInNjb3BlIjoidGFza3M6cmVhZCJ9.c2lnbmF0dXJl"
	p, err := svc.Resolve(context.Background(), jwt)
	if p != nil || err == nil {
		t.Fatalf("a JWT bearer must be denied (opaque tokens only); got p=%v err=%v", p, err)
	}
}
