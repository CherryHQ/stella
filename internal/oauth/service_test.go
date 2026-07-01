package oauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/CherryHQ/stella/internal/credential"
)

// memStore is an in-memory oauth.Store for flow tests. It models the single-use
// and rotation semantics the real SQL enforces (unique constraints + consumed_at).
type memStore struct {
	clients  map[string]Client
	codes    map[string]AuthCodeCreate // by code_hash; deleted on consume
	refresh  map[string]*refreshRow    // by public_id
	seq      int
	revokedA map[string]bool // access-token families revoked (by family id)
}

type refreshRow struct {
	rec      RefreshRecord
	consumed bool
	revoked  bool
}

func newMemStore() *memStore {
	return &memStore{
		clients: map[string]Client{}, codes: map[string]AuthCodeCreate{},
		refresh: map[string]*refreshRow{}, revokedA: map[string]bool{},
	}
}

func (m *memStore) CreateClient(_ context.Context, c ClientCreate) (Client, error) {
	cl := Client{
		ClientID: c.ClientID, Name: c.Name, ClientSecretHash: c.ClientSecretHash,
		ClientType: c.ClientType, RedirectURIs: c.RedirectURIs, GrantTypes: c.GrantTypes,
		Scopes: c.Scopes, OwnerUserID: c.OwnerUserID, CreatedAt: time.Now(),
	}
	m.clients[c.ClientID] = cl
	return cl, nil
}

func (m *memStore) GetClient(_ context.Context, clientID string) (Client, error) {
	c, ok := m.clients[clientID]
	if !ok {
		return Client{}, errors.New("not found")
	}
	return c, nil
}

func (m *memStore) ListClientsByOwner(_ context.Context, owner string) ([]Client, error) {
	var out []Client
	for _, c := range m.clients {
		if c.OwnerUserID == owner {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *memStore) UpdateClientSecret(_ context.Context, clientID, owner, hash string) (int64, error) {
	c, ok := m.clients[clientID]
	if !ok || c.OwnerUserID != owner {
		return 0, nil
	}
	c.ClientSecretHash = hash
	m.clients[clientID] = c
	return 1, nil
}

func (m *memStore) DisableClient(_ context.Context, clientID, owner string) (int64, error) {
	c, ok := m.clients[clientID]
	if !ok || c.OwnerUserID != owner || c.Disabled {
		return 0, nil
	}
	c.Disabled = true
	m.clients[clientID] = c
	return 1, nil
}

func (m *memStore) CreateCode(_ context.Context, c AuthCodeCreate) error {
	m.codes[c.CodeHash] = c
	return nil
}

func (m *memStore) ConsumeCode(_ context.Context, codeHash string) (AuthCode, bool, error) {
	c, ok := m.codes[codeHash]
	if !ok {
		return AuthCode{}, false, nil
	}
	delete(m.codes, codeHash) // single-use
	return AuthCode{
		ClientID: c.ClientID, UserID: c.UserID, RedirectURI: c.RedirectURI, Scopes: c.Scopes,
		CodeChallenge: c.CodeChallenge, CodeChallengeMethod: c.CodeChallengeMethod, ExpiresAt: c.ExpiresAt,
	}, true, nil
}

func (m *memStore) CreateRefresh(_ context.Context, r RefreshCreate) (RefreshRecord, error) {
	m.seq++
	fam := r.FamilyID
	if fam == "" {
		fam = fmt.Sprintf("fam-%d", m.seq)
	}
	rec := RefreshRecord{
		ID: fmt.Sprintf("rid-%d", m.seq), PublicID: r.PublicID, TokenHash: r.TokenHash,
		ClientID: r.ClientID, UserID: r.UserID, Scopes: r.Scopes, FamilyID: fam, ExpiresAt: r.ExpiresAt,
	}
	m.refresh[r.PublicID] = &refreshRow{rec: rec}
	return rec, nil
}

func (m *memStore) GetRefreshByPublicID(_ context.Context, publicID string) (RefreshRecord, error) {
	row, ok := m.refresh[publicID]
	if !ok {
		return RefreshRecord{}, errors.New("not found")
	}
	rec := row.rec
	rec.Consumed = row.consumed
	rec.Revoked = row.revoked
	return rec, nil
}

func (m *memStore) ConsumeRefresh(_ context.Context, publicID, _ string) (RefreshRecord, bool, error) {
	row, ok := m.refresh[publicID]
	if !ok || row.consumed || row.revoked {
		return RefreshRecord{}, false, nil
	}
	row.consumed = true
	return row.rec, true, nil
}

func (m *memStore) RevokeRefreshFamily(_ context.Context, familyID string) (int64, error) {
	var n int64
	for _, row := range m.refresh {
		if row.rec.FamilyID == familyID && !row.revoked {
			row.revoked = true
			n++
		}
	}
	return n, nil
}

func (m *memStore) ListAuthorizedApps(context.Context, string) ([]AuthorizedApp, error) {
	return nil, nil
}
func (m *memStore) RevokeGrantForUser(context.Context, string, string) (int64, error) { return 0, nil }

func (m *memStore) RevokeAccessByFamily(_ context.Context, familyID string) (int64, error) {
	m.revokedA[familyID] = true
	return 1, nil
}

func (m *memStore) RevokeAccessForUserClient(context.Context, string, string) (int64, error) {
	return 0, nil
}

// fakeIssuer records the access tokens minted through the credential front door.
type fakeIssuer struct{ families []string }

func (f *fakeIssuer) IssueOAuthAccess(_ context.Context, userID, clientID string, scopes []string, family string, _ time.Duration) (string, credential.OAuthAccessRecord, error) {
	f.families = append(f.families, family)
	return "stella_oat_fake_" + family, credential.OAuthAccessRecord{UserID: userID, ClientID: clientID, Scopes: scopes}, nil
}

func newTestFlow() (*Service, *memStore, *fakeIssuer) {
	store := newMemStore()
	iss := &fakeIssuer{}
	svc := NewService(Config{Store: store, Issuer: iss})
	return svc, store, iss
}

func registerConfidential(t *testing.T, svc *Service) (Client, string) {
	t.Helper()
	client, secret, err := svc.RegisterClient(context.Background(), "u1", ClientRegistration{
		Name: "Test App", ClientType: ClientTypeConfidential,
		RedirectURIs: []string{"https://app.example/cb"},
		Scopes:       []string{"tasks:read", "tasks:write"},
	})
	if err != nil {
		t.Fatalf("register client: %v", err)
	}
	return client, secret
}

func TestAuthorizationCodePKCEHappyPath(t *testing.T) {
	svc, _, iss := newTestFlow()
	ctx := context.Background()
	client, secret := registerConfidential(t, svc)

	verifier := "a-high-entropy-code-verifier-value-1234567890"
	challenge := oidc.NewSHACodeChallenge(verifier)
	req := AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://app.example/cb",
		ResponseType: "code", Scopes: []string{"tasks:read"}, State: "xyz",
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
	}
	authCtx, err := svc.Authorize(ctx, req)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if len(authCtx.Scopes) != 1 || authCtx.Scopes[0] != "tasks:read" {
		t.Fatalf("unexpected consent scopes: %v", authCtx.Scopes)
	}

	code, err := svc.IssueCode(ctx, "u1", req, authCtx.Scopes)
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}

	res, err := svc.Exchange(ctx, TokenRequest{
		GrantType: "authorization_code", ClientID: client.ClientID, ClientSecret: secret,
		Code: code, RedirectURI: "https://app.example/cb", CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("exchange code: %v", err)
	}
	if !strings.HasPrefix(res.AccessToken, "stella_oat_") {
		t.Fatalf("access token must be opaque stella_oat_, got %q", res.AccessToken)
	}
	if !strings.HasPrefix(res.RefreshToken, "stella_ort_") {
		t.Fatalf("refresh token must be stella_ort_, got %q", res.RefreshToken)
	}
	if len(iss.families) != 1 || iss.families[0] == "" {
		t.Fatalf("access token must be minted with a refresh family, got %v", iss.families)
	}

	// Single-use: replaying the same code fails.
	if _, err := svc.Exchange(ctx, TokenRequest{
		GrantType: "authorization_code", ClientID: client.ClientID, ClientSecret: secret,
		Code: code, RedirectURI: "https://app.example/cb", CodeVerifier: verifier,
	}); err == nil {
		t.Fatal("authorization code must be single-use")
	}
}

func TestPKCEWrongVerifierRejected(t *testing.T) {
	svc, _, _ := newTestFlow()
	ctx := context.Background()
	client, secret := registerConfidential(t, svc)
	req := AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://app.example/cb", ResponseType: "code",
		Scopes: []string{"tasks:read"}, CodeChallenge: oidc.NewSHACodeChallenge("right-verifier"), CodeChallengeMethod: "S256",
	}
	if _, err := svc.Authorize(ctx, req); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	code, _ := svc.IssueCode(ctx, "u1", req, []string{"tasks:read"})
	_, err := svc.Exchange(ctx, TokenRequest{
		GrantType: "authorization_code", ClientID: client.ClientID, ClientSecret: secret,
		Code: code, RedirectURI: "https://app.example/cb", CodeVerifier: "wrong-verifier",
	})
	if err == nil {
		t.Fatal("PKCE with the wrong verifier must fail")
	}
}

func TestRefreshRotationAndReuseRevokesFamily(t *testing.T) {
	svc, store, _ := newTestFlow()
	ctx := context.Background()
	client, secret := registerConfidential(t, svc)
	req := AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://app.example/cb", ResponseType: "code",
		Scopes: []string{"tasks:read"},
	}
	if _, err := svc.Authorize(ctx, req); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	code, _ := svc.IssueCode(ctx, "u1", req, []string{"tasks:read"})
	first, err := svc.Exchange(ctx, TokenRequest{
		GrantType: "authorization_code", ClientID: client.ClientID, ClientSecret: secret,
		Code: code, RedirectURI: "https://app.example/cb",
	})
	if err != nil {
		t.Fatalf("initial exchange: %v", err)
	}

	// Rotate: old refresh -> new refresh.
	rotated, err := svc.Exchange(ctx, TokenRequest{
		GrantType: "refresh_token", ClientID: client.ClientID, ClientSecret: secret,
		RefreshToken: first.RefreshToken,
	})
	if err != nil {
		t.Fatalf("refresh rotation: %v", err)
	}
	if rotated.RefreshToken == first.RefreshToken {
		t.Fatal("rotation must issue a new refresh token")
	}

	// Reuse the consumed old refresh token -> family revoked.
	_, err = svc.Exchange(ctx, TokenRequest{
		GrantType: "refresh_token", ClientID: client.ClientID, ClientSecret: secret,
		RefreshToken: first.RefreshToken,
	})
	if err == nil {
		t.Fatal("reusing a consumed refresh token must fail")
	}

	// The rotated (still-valid-looking) token must now be revoked too.
	if _, err := svc.Exchange(ctx, TokenRequest{
		GrantType: "refresh_token", ClientID: client.ClientID, ClientSecret: secret,
		RefreshToken: rotated.RefreshToken,
	}); err == nil {
		t.Fatal("reuse detection must revoke the whole family")
	}
	// Access tokens in the family are revoked via cascade.
	if len(store.revokedA) == 0 {
		t.Fatal("access-token family cascade revoke must have run")
	}
}

func TestScopeSubsetEnforced(t *testing.T) {
	svc, _, _ := newTestFlow()
	ctx := context.Background()
	client, _ := registerConfidential(t, svc) // client allowed tasks:read, tasks:write

	// Requesting a scope the client is not allowed -> invalid_scope redirect error.
	_, err := svc.Authorize(ctx, AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://app.example/cb", ResponseType: "code",
		Scopes: []string{"email:read"},
	})
	var redirErr *RedirectError
	if !errors.As(err, &redirErr) || redirErr.Code != string(oidc.InvalidScope) {
		t.Fatalf("out-of-client-scope request must be invalid_scope, got %v", err)
	}

	// A non-exposable scope (vault) is never grantable.
	_, err = svc.Authorize(ctx, AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://app.example/cb", ResponseType: "code",
		Scopes: []string{"vault:read"},
	})
	if !errors.As(err, &redirErr) || redirErr.Code != string(oidc.InvalidScope) {
		t.Fatalf("non-exposable scope must be invalid_scope, got %v", err)
	}
}

func TestPublicClientRequiresPKCE(t *testing.T) {
	svc, _, _ := newTestFlow()
	ctx := context.Background()
	client, _, err := svc.RegisterClient(ctx, "u1", ClientRegistration{
		Name: "SPA", ClientType: ClientTypePublic,
		RedirectURIs: []string{"https://spa.example/cb"}, Scopes: []string{"tasks:read"},
	})
	if err != nil {
		t.Fatalf("register public client: %v", err)
	}
	_, err = svc.Authorize(ctx, AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://spa.example/cb", ResponseType: "code",
		Scopes: []string{"tasks:read"}, // no code_challenge
	})
	var redirErr *RedirectError
	if !errors.As(err, &redirErr) {
		t.Fatalf("public client without PKCE must error, got %v", err)
	}
}

func TestUnknownRedirectURINotRedirected(t *testing.T) {
	svc, _, _ := newTestFlow()
	client, _ := registerConfidential(t, svc)
	_, err := svc.Authorize(context.Background(), AuthorizeRequest{
		ClientID: client.ClientID, RedirectURI: "https://evil.example/cb", ResponseType: "code",
		Scopes: []string{"tasks:read"},
	})
	var redirErr *RedirectError
	if err == nil || errors.As(err, &redirErr) {
		t.Fatalf("unregistered redirect_uri must be a hard error, not a redirect; got %v", err)
	}
}
