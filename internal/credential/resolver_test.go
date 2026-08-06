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
		if r.UserID == userID && r.TokenUse == TokenUsePersonal {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) ListProvisioningTokenByUser(_ context.Context, userID string) ([]PATRecord, error) {
	var out []PATRecord
	for _, r := range f.byPublicID {
		if r.UserID == userID && r.TokenUse == TokenUseProvisioning {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) RevokePAT(_ context.Context, id, userID string) (int64, error) {
	for k, r := range f.byPublicID {
		if r.ID == id && r.UserID == userID && r.TokenUse == TokenUsePersonal && r.RevokedAt == nil {
			now := time.Now()
			r.RevokedAt = &now
			f.byPublicID[k] = r
			return 1, nil
		}
	}
	return 0, nil
}

func (f *fakeStore) RevokeProvisioningToken(_ context.Context, id, userID string) (int64, error) {
	for k, r := range f.byPublicID {
		if r.ID == id && r.UserID == userID && r.TokenUse == TokenUseProvisioning && r.RevokedAt == nil {
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

func newTestService(t *testing.T) (*Service, *fakeStore) {
	store := &fakeStore{
		byPublicID: map[string]PATRecord{},
		users:      map[string]Identity{"u1": {UserID: "u1", Email: "u1@x", IsActive: true, Role: "user"}},
	}
	svc := NewService(Config{PATs: store, Users: store})
	return svc, store
}

func TestResolvePATRoundTrip(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	plaintext, rec, err := svc.CreatePAT(ctx, "u1", "ci", nil)
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
	if len(p.Scopes) != 0 {
		t.Fatalf("resolved PAT scopes = %v, want empty", p.Scopes)
	}
	if len(rec.Scopes) != 0 {
		t.Fatalf("new PAT scopes = %v, want empty legacy storage value", rec.Scopes)
	}
	legacy := store.byPublicID[rec.PublicID]
	legacy.Scopes = []string{"goals:read"}
	store.byPublicID[rec.PublicID] = legacy
	p, err = svc.Resolve(ctx, "Bearer "+plaintext)
	if err != nil {
		t.Fatalf("resolve legacy PAT: %v", err)
	}
	if len(p.Scopes) != 0 {
		t.Fatalf("resolved legacy PAT scopes = %v, want empty", p.Scopes)
	}
	if len(store.touched) == 0 {
		t.Fatal("resolve should throttle-touch last_used")
	}
}

func TestResolvePATUsesCurrentOwnerAuthority(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	plaintext, _, err := svc.CreatePAT(ctx, "u1", "admin", nil)
	if err != nil {
		t.Fatalf("create PAT: %v", err)
	}

	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: true, Role: "admin", IsAdmin: true}
	p, err := svc.Resolve(ctx, "Bearer "+plaintext)
	if err != nil || !p.IsAdmin {
		t.Fatalf("promoted owner PAT = %+v, %v; want current admin authority", p, err)
	}

	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: true, Role: "user"}
	p, err = svc.Resolve(ctx, "Bearer "+plaintext)
	if err != nil || p.IsAdmin {
		t.Fatalf("demoted owner PAT = %+v, %v; must lose admin authority", p, err)
	}
}

// CRITICAL #3: a malformed/unknown stella_pat_ bearer must NOT fall through to
// any other credential family.
func TestResolveMalformedPATDoesNotFallThrough(t *testing.T) {
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
	plaintext, rec, _ := svc.CreatePAT(ctx, "u1", "r", nil)
	now := time.Now()
	r := store.byPublicID[rec.PublicID]
	r.RevokedAt = &now
	store.byPublicID[rec.PublicID] = r
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext); err == nil {
		t.Fatal("revoked PAT must be denied")
	}

	// Expired.
	past := time.Now().Add(-time.Hour)
	plaintext2, _, _ := svc.CreatePAT(ctx, "u1", "e", &past)
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

func TestProvisioningTokenRequiresActiveAdminAndCarriesCredentialID(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: true, Role: "admin", IsAdmin: true}

	plaintext, rec, err := svc.CreateProvisioningToken(ctx, "u1", "scim", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create provisioning token: %v", err)
	}
	p, err := svc.Resolve(ctx, "Bearer "+plaintext)
	if err != nil {
		t.Fatalf("resolve provisioning token: %v", err)
	}
	if p.Kind != KindProvisioning || p.CredentialID != rec.ID || len(p.Scopes) != 0 {
		t.Fatalf("provisioning principal = %+v, want kind, credential ID, and no scopes", p)
	}

	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: true, Role: "user"}
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext); err == nil {
		t.Fatal("demoted provisioning owner must be denied")
	}
	if _, _, err := svc.CreateProvisioningToken(ctx, "u1", "after-demotion", time.Now().Add(time.Hour)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("demoted owner create error = %v, want ErrForbidden", err)
	}
	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: false, Role: "admin", IsAdmin: true}
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext); err == nil {
		t.Fatal("deactivated provisioning owner must be denied")
	}
}

func TestProvisioningTokenActiveLimit(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: true, Role: "admin", IsAdmin: true}

	for _, name := range []string{"current", "rotation-overlap"} {
		if _, _, err := svc.CreateProvisioningToken(ctx, "u1", name, time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("create %s provisioning token: %v", name, err)
		}
	}
	if _, _, err := svc.CreateProvisioningToken(ctx, "u1", "third", time.Now().Add(time.Hour)); !errors.Is(err, ErrProvisioningTokenLimit) {
		t.Fatalf("third active provisioning token error = %v, want ErrProvisioningTokenLimit", err)
	}
}

func TestProvisioningTokenRejectsPurposeMismatchExpiryAndRevocation(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	store.users["u1"] = Identity{UserID: "u1", Email: "u1@x", IsActive: true, Role: "admin", IsAdmin: true}

	plaintext, rec, err := svc.CreateProvisioningToken(ctx, "u1", "scim", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create provisioning token: %v", err)
	}
	mismatch := store.byPublicID[rec.PublicID]
	mismatch.TokenUse = TokenUsePersonal
	store.byPublicID[rec.PublicID] = mismatch
	if _, err := svc.Resolve(ctx, "Bearer "+plaintext); err == nil {
		t.Fatal("provisioning prefix with personal stored use must be denied")
	}

	expired, expiredRec, err := svc.CreateProvisioningToken(ctx, "u1", "expired", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create expired fixture: %v", err)
	}
	r := store.byPublicID[expiredRec.PublicID]
	past := time.Now().Add(-time.Hour)
	r.ExpiresAt = &past
	store.byPublicID[expiredRec.PublicID] = r
	if _, err := svc.Resolve(ctx, "Bearer "+expired); err == nil {
		t.Fatal("expired provisioning token must be denied")
	}

	revoked, revokedRec, err := svc.CreateProvisioningToken(ctx, "u1", "revoked", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create revoked fixture: %v", err)
	}
	if ok, err := svc.RevokeProvisioningToken(ctx, revokedRec.ID, "u1"); err != nil || !ok {
		t.Fatalf("revoke provisioning token = %v, %v", ok, err)
	}
	if _, err := svc.Resolve(ctx, "Bearer "+revoked); err == nil {
		t.Fatal("revoked provisioning token must be denied")
	}
}

func TestTokenUseValidationHasOneAuthority(t *testing.T) {
	for _, tc := range []struct {
		use  TokenUse
		want bool
	}{
		{TokenUsePersonal, true},
		{TokenUseProvisioning, true},
		{TokenUse("unknown"), false},
	} {
		if got := tc.use.Valid(); got != tc.want {
			t.Errorf("TokenUse(%q).Valid() = %v, want %v", tc.use, got, tc.want)
		}
	}
	svc, store := newTestService(t)
	if _, _, err := svc.createToken(context.Background(), KindPAT, TokenUse("unknown"), "u1", "bad", nil); err == nil {
		t.Fatal("invalid token use must not reach persistence")
	}
	if len(store.byPublicID) != 0 {
		t.Fatal("invalid token use persisted a record")
	}
}
