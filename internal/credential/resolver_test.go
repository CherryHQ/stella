package credential

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore is an in-memory PATStore + UserLookup for resolver tests.
type fakeStore struct {
	byPublicID map[string]PATRecord
	users      map[string]Identity
	touched    []string
}

func (f *fakeStore) CreatePAT(_ context.Context, rec PATRecord) (PATRecord, error) {
	if f.byPublicID == nil {
		f.byPublicID = map[string]PATRecord{}
	}
	rec.ID = "id-" + rec.PublicID
	f.byPublicID[rec.PublicID] = rec
	return rec, nil
}

func (f *fakeStore) GetPATByPublicID(_ context.Context, publicID string) (PATRecord, error) {
	rec, ok := f.byPublicID[publicID]
	if !ok {
		return PATRecord{}, errors.New("not found")
	}
	return rec, nil
}

func (f *fakeStore) ListPATByUser(_ context.Context, userID string) ([]PATRecord, error) {
	var out []PATRecord
	for _, r := range f.byPublicID {
		if r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) RevokePAT(_ context.Context, id, userID string) (int64, error) {
	for k, r := range f.byPublicID {
		if r.ID == id && r.UserID == userID && r.RevokedAt == nil {
			now := time.Now()
			r.RevokedAt = &now
			f.byPublicID[k] = r
			return 1, nil
		}
	}
	return 0, nil
}

func (f *fakeStore) RevokePATByUser(_ context.Context, userID string) (int64, error) {
	var n int64
	for k, r := range f.byPublicID {
		if r.UserID == userID && r.RevokedAt == nil {
			now := time.Now()
			r.RevokedAt = &now
			f.byPublicID[k] = r
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) TouchPATLastUsed(_ context.Context, id string) (int64, error) {
	f.touched = append(f.touched, id)
	return 1, nil
}

func (f *fakeStore) LookupUser(_ context.Context, userID string) (Identity, error) {
	id, ok := f.users[userID]
	if !ok {
		return Identity{}, errors.New("no user")
	}
	return id, nil
}

// legacyTrap fails the test if the legacy full-access path is ever reached with a
// PAT/OAuth-prefixed token -- the whole point of CRITICAL #3.
type legacyTrap struct {
	t          *testing.T
	legacyCall int
}

func (l *legacyTrap) AuthenticateLegacy(_ context.Context, raw string) (Identity, error) {
	l.legacyCall++
	l.t.Fatalf("legacy Authenticate must never be reached for %q", raw)
	return Identity{}, nil
}

func (l *legacyTrap) AuthenticateScoped(_ context.Context, _ string) (ScopedResult, error) {
	return ScopedResult{}, errors.New("not scoped")
}

func newTestService(t *testing.T) (*Service, *fakeStore) {
	store := &fakeStore{
		byPublicID: map[string]PATRecord{},
		users:      map[string]Identity{"u1": {UserID: "u1", Email: "u1@x", IsActive: true, Role: "user"}},
	}
	svc := NewService(Config{PATs: store, Users: store, Tokens: &legacyTrap{t: t}})
	return svc, store
}

func TestResolvePATRoundTrip(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	plaintext, rec, err := svc.CreatePAT(ctx, "u1", "ci", []string{"goals:read"}, nil)
	if err != nil {
		t.Fatalf("create PAT: %v", err)
	}
	if rec.PublicID == "" || rec.TokenHash == "" {
		t.Fatal("created record missing public id or hash")
	}

	p, err := svc.Resolve(ctx, "Bearer "+plaintext)
	if err != nil {
		t.Fatalf("resolve valid PAT: %v", err)
	}
	if p == nil || p.Kind != KindPAT || p.UserID != "u1" {
		t.Fatalf("unexpected principal: %+v", p)
	}
	if p.IsAdmin {
		t.Fatal("PATs must never carry admin in phase 1")
	}
	if len(store.touched) == 0 {
		t.Fatal("resolve should throttle-touch last_used")
	}
}

// CRITICAL #3: a malformed/unknown stella_pat_ bearer must NOT fall through to
// the legacy Authenticate path. legacyTrap.Fatalf enforces that.
func TestResolveMalformedPATDoesNotFallThroughToLegacy(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	for _, raw := range []string{
		"Bearer stella_pat_deadbeef_notarealsecret",
		"Bearer stella_pat_",
		"Bearer stella_pat_abc_" + "x", // wrong length secret+crc
		"Bearer stella_oat_someoauthaccesstoken",
	} {
		p, err := svc.Resolve(ctx, raw)
		if p != nil {
			t.Fatalf("malformed bearer %q must not resolve to a principal", raw)
		}
		if err == nil {
			t.Fatalf("malformed bearer %q must return an error (hard deny)", raw)
		}
	}
}

// A well-formed PAT whose public_id is unknown must be denied, never fall
// through to legacy.
func TestResolveUnknownPATDenied(t *testing.T) {
	svc, _ := newTestService(t)
	minted, err := MintOpaque(KindPAT)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Resolve(context.Background(), "Bearer "+minted.Plaintext)
	if p != nil || err == nil {
		t.Fatalf("unknown PAT must be denied; got p=%v err=%v", p, err)
	}
}

func TestResolveRevokedAndExpiredPAT(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	// Revoked.
	plaintext, rec, _ := svc.CreatePAT(ctx, "u1", "r", []string{"goals:read"}, nil)
	now := time.Now()
	r := store.byPublicID[rec.PublicID]
	r.RevokedAt = &now
	store.byPublicID[rec.PublicID] = r
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext); err == nil {
		t.Fatal("revoked PAT must be denied")
	}

	// Expired.
	past := time.Now().Add(-time.Hour)
	plaintext2, _, _ := svc.CreatePAT(ctx, "u1", "e", []string{"goals:read"}, &past)
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext2); err == nil {
		t.Fatal("expired PAT must be denied")
	}
}

func TestResolveNonBearerReturnsNilNil(t *testing.T) {
	svc, _ := newTestService(t)
	p, err := svc.Resolve(context.Background(), "")
	if p != nil || err != nil {
		t.Fatalf("no bearer should be (nil, nil); got p=%v err=%v", p, err)
	}
	p, err = svc.Resolve(context.Background(), "Basic abc")
	if p != nil || err != nil {
		t.Fatalf("non-bearer scheme should be (nil, nil); got p=%v err=%v", p, err)
	}
}

func TestResolveEmptyBearerHardRejected(t *testing.T) {
	svc, _ := newTestService(t)
	for _, header := range []string{"Bearer", "Bearer   "} {
		p, err := svc.Resolve(context.Background(), header)
		if p != nil || err == nil {
			t.Fatalf("empty bearer %q must be hard-rejected; got p=%v err=%v", header, p, err)
		}
	}
}

func TestResolveRefreshTokenHardRejected(t *testing.T) {
	svc, _ := newTestService(t)
	p, err := svc.Resolve(context.Background(), "Bearer stella_ort_refreshtoken")
	if p != nil || err == nil {
		t.Fatalf("refresh token must be hard-rejected at the API boundary; got p=%v err=%v", p, err)
	}
}
