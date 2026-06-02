package local_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
)

// --- in-memory fakes ---

type fakeUserStore struct{ byID, byEmail map[string]auth.User }

func newFakeUserStore(users ...auth.User) *fakeUserStore {
	f := &fakeUserStore{byID: make(map[string]auth.User), byEmail: make(map[string]auth.User)}
	for _, u := range users {
		f.byID[u.ID] = u
		f.byEmail[u.Email] = u
	}
	return f
}

func (f *fakeUserStore) GetUser(_ context.Context, id string) (auth.User, error) {
	u, ok := f.byID[id]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}

func (f *fakeUserStore) GetUserByEmail(_ context.Context, email string) (auth.User, error) {
	u, ok := f.byEmail[email]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}

func (f *fakeUserStore) CreateUser(_ context.Context, u auth.User) (auth.User, error) {
	if u.IsActive == false {
		u.IsActive = true
	}
	f.byID[u.ID] = u
	f.byEmail[u.Email] = u
	return u, nil
}
func (f *fakeUserStore) ListUsers(_ context.Context) ([]auth.User, error) { return nil, nil }
func (f *fakeUserStore) ListUsersPaged(_ context.Context, _, _ int64) ([]auth.User, error) {
	return nil, nil
}
func (f *fakeUserStore) UpdateUser(_ context.Context, _ auth.User) error             { return nil }
func (f *fakeUserStore) DeleteUser(_ context.Context, _ string) error                { return nil }
func (f *fakeUserStore) CountUsers(_ context.Context) (int64, error)                 { return int64(len(f.byID)), nil }
func (f *fakeUserStore) UpdateUserAgeKeys(_ context.Context, _, _, _ string) error   { return nil }
func (f *fakeUserStore) UpdateUserDefaultAgent(_ context.Context, _, _ string) error { return nil }
func (f *fakeUserStore) UpdateUserNotifyIdentity(_ context.Context, _ string, _ *string) error {
	return nil
}
func (f *fakeUserStore) UpdateUserRole(_ context.Context, _ string, _ string) error { return nil }
func (f *fakeUserStore) UpdateUserActive(_ context.Context, _ string, _ bool) error { return nil }

type fakeCredStore struct{ byUser map[string]auth.Credential }

func newFakeCredStore(creds ...auth.Credential) *fakeCredStore {
	f := &fakeCredStore{byUser: make(map[string]auth.Credential)}
	for _, c := range creds {
		f.byUser[c.UserID] = c
	}
	return f
}

func (f *fakeCredStore) CreateCredential(_ context.Context, c auth.Credential) (auth.Credential, error) {
	f.byUser[c.UserID] = c
	return c, nil
}

func (f *fakeCredStore) GetCredentialByUserID(_ context.Context, userID string) (auth.Credential, error) {
	c, ok := f.byUser[userID]
	if !ok {
		return auth.Credential{}, auth.ErrNotFound
	}
	return c, nil
}

func (f *fakeCredStore) UpdateCredentialHash(_ context.Context, userID, hash string) error {
	c := f.byUser[userID]
	c.PasswordHash = hash
	f.byUser[userID] = c
	return nil
}

func (f *fakeCredStore) DeleteCredential(_ context.Context, userID string) error {
	delete(f.byUser, userID)
	return nil
}

type fakeCodeStore struct{ codes map[string]auth.OIDCCode }

func newFakeCodeStore() *fakeCodeStore {
	return &fakeCodeStore{codes: make(map[string]auth.OIDCCode)}
}

func (f *fakeCodeStore) CreateOIDCCode(_ context.Context, c auth.OIDCCode) (auth.OIDCCode, error) {
	f.codes[c.CodeHash] = c
	return c, nil
}

func (f *fakeCodeStore) ConsumeOIDCCode(_ context.Context, hash string) (auth.OIDCCode, error) {
	c, ok := f.codes[hash]
	if !ok {
		return auth.OIDCCode{}, auth.ErrNotFound
	}
	if c.ConsumedAt != nil {
		return auth.OIDCCode{}, auth.ErrAlreadyConsumed
	}
	if time.Now().After(c.ExpiresAt) {
		return auth.OIDCCode{}, auth.ErrExpired
	}
	now := time.Now()
	c.ConsumedAt = &now
	f.codes[hash] = c
	return c, nil
}

type fakeTokenStore struct {
	tokens map[string]auth.OIDCAccessToken
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{tokens: make(map[string]auth.OIDCAccessToken)}
}

func (f *fakeTokenStore) CreateOIDCAccessToken(_ context.Context, t auth.OIDCAccessToken) (auth.OIDCAccessToken, error) {
	f.tokens[t.TokenHash] = t
	return t, nil
}

func (f *fakeTokenStore) GetOIDCAccessTokenByHash(_ context.Context, hash string) (auth.OIDCAccessToken, error) {
	t, ok := f.tokens[hash]
	if !ok {
		return auth.OIDCAccessToken{}, auth.ErrNotFound
	}
	return t, nil
}
func (f *fakeTokenStore) DeleteExpiredOIDCAccessTokens(_ context.Context) error { return nil }

// --- test helpers ---

func generateTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func keyToPEM(key *ecdsa.PrivateKey) string {
	der, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func confidentialConfig(t *testing.T, key *ecdsa.PrivateKey) *local.Config {
	t.Helper()
	return &local.Config{
		IssuerURL:      "http://localhost:25678/oidc/local",
		ClientID:       "test-client",
		ClientSecret:   "test-secret",
		RedirectURIs:   []string{"http://localhost/callback"},
		SigningKey:     key,
		KeyID:          "k1",
		AccessTokenTTL: 3600,
		AuthCodeTTL:    120,
	}
}

func publicConfig(t *testing.T, key *ecdsa.PrivateKey) *local.Config {
	t.Helper()
	return &local.Config{
		IssuerURL:      "http://localhost:25678/oidc/local",
		ClientID:       "test-client",
		RedirectURIs:   []string{"http://localhost/callback"},
		SigningKey:     key,
		KeyID:          "k1",
		AccessTokenTTL: 3600,
		AuthCodeTTL:    120,
	}
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func seedUserAndCreds(userID, email, password string) (*fakeUserStore, *fakeCredStore) {
	hash, _ := auth.HashPassword(password)
	users := newFakeUserStore(auth.User{ID: userID, Email: email, Name: "Test User", IsActive: true})
	creds := newFakeCredStore(auth.Credential{ID: uuid.NewString(), UserID: userID, PasswordHash: hash})
	return users, creds
}

func issueLoginCode(t *testing.T, cfg *local.Config, codes *fakeCodeStore, users *fakeUserStore, creds *fakeCredStore, email, password string) (string, string) {
	t.Helper()
	verifier := strings.Repeat("v", 43)
	svc := local.NewService(cfg, codes, users, creds)
	redirectURL, err := svc.Login(context.Background(), local.LoginInput{
		Email:    email,
		Password: password,
		State: auth.AuthState{
			CodeVerifier: verifier,
			ProviderName: "local",
		},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	loc, _ := url.Parse(redirectURL)
	return loc.Query().Get("code"), verifier
}

func newTestIssuer(cfg *local.Config, codes *fakeCodeStore, tokens *fakeTokenStore, users *fakeUserStore, creds *fakeCredStore) *local.Issuer {
	svc := local.NewService(cfg, codes, users, creds)
	return local.NewIssuer(cfg, codes, tokens, users, svc, nil, nil)
}

// --- tests ---

func TestDiscovery(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	r := httptest.NewRequest(http.MethodGet, "/oidc/local/.well-known/openid-configuration", nil)
	w := httptest.NewRecorder()
	issuer.HandleDiscovery(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	var doc local.DiscoveryDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal discovery: %v", err)
	}
	if doc.Issuer != cfg.IssuerURL {
		t.Errorf("issuer %q, want %q", doc.Issuer, cfg.IssuerURL)
	}
	if !strings.Contains(doc.AuthorizationEndpoint, "/authorize") {
		t.Errorf("unexpected auth endpoint: %s", doc.AuthorizationEndpoint)
	}
	if !strings.Contains(doc.JWKSUri, "/jwks.json") {
		t.Errorf("unexpected jwks uri: %s", doc.JWKSUri)
	}
}

func TestJWKS(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	r := httptest.NewRequest(http.MethodGet, "/oidc/local/jwks.json", nil)
	w := httptest.NewRecorder()
	issuer.HandleJWKS(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	var resp map[string][]local.JWK
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal jwks: %v", err)
	}
	keys := resp["keys"]
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if keys[0].Kty != "EC" || keys[0].Crv != "P-256" {
		t.Errorf("unexpected key: kty=%s crv=%s", keys[0].Kty, keys[0].Crv)
	}
	if keys[0].Kid != "k1" {
		t.Errorf("kid %q, want k1", keys[0].Kid)
	}
}

func TestAuthorizeRejectsUnknownRedirectURI(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {"http://evil.example/callback"},
		"response_type": {"code"},
		"scope":         {"openid"},
	}
	r := httptest.NewRequest(http.MethodGet, "/oidc/local/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	issuer.HandleAuthorize(w, r)

	if w.Code == http.StatusFound {
		t.Error("should not redirect for unknown redirect_uri")
	}
}

func TestAuthorizeRedirectsToLoginWithNoSession(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"response_type": {"code"},
		"scope":         {"openid email"},
	}

	r := httptest.NewRequest(http.MethodGet, "/oidc/local/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	issuer.HandleAuthorize(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", w.Code)
	}
	loc := w.Result().Header.Get("Location")
	if !strings.HasPrefix(loc, "/login?") {
		t.Errorf("unexpected redirect location: %s", loc)
	}
}

func TestAuthorizeRedirectsToSignup(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"response_type": {"code"},
		"scope":         {"openid email"},
		"mode":          {"register"},
	}

	r := httptest.NewRequest(http.MethodGet, "/oidc/local/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	issuer.HandleAuthorize(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", w.Code)
	}
	loc := w.Result().Header.Get("Location")
	if !strings.HasPrefix(loc, "/signup?") {
		t.Errorf("unexpected redirect location for register mode: %s", loc)
	}
}

func TestAuthorizePostRejected(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"response_type": {"code"},
		"scope":         {"openid email"},
	}
	r := httptest.NewRequest(http.MethodPost, "/oidc/local/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	issuer.HandleAuthorize(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", w.Code)
	}
}

func TestLocalServiceLoginIssuesCode(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	userID := uuid.NewString()
	users, creds := seedUserAndCreds(userID, "user@test.example", "pass123")
	codeStore := newFakeCodeStore()

	svc := local.NewService(cfg, codeStore, users, creds)
	redirectURL, err := svc.Login(context.Background(), local.LoginInput{
		Email:    "user@test.example",
		Password: "pass123",
		State: auth.AuthState{
			State:        "mystate",
			CodeVerifier: strings.Repeat("a", 43),
			ProviderName: "local",
		},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	u, _ := url.Parse(redirectURL)
	if u.Query().Get("code") == "" {
		t.Errorf("no code in redirect: %s", redirectURL)
	}
	if u.Query().Get("state") != "mystate" {
		t.Errorf("state mismatch: %s", redirectURL)
	}
}

func TestLocalServiceRegisterBootstrapOnly(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	cfg.AllowRegistration = true
	users := newFakeUserStore()
	creds := newFakeCredStore()
	codeStore := newFakeCodeStore()
	state := auth.AuthState{State: "registerstate", CodeVerifier: strings.Repeat("b", 43), ProviderName: "local"}

	svc := local.NewService(cfg, codeStore, users, creds)
	redirectURL, err := svc.Register(context.Background(), local.RegisterInput{
		Name:            "Test User",
		Email:           "register@test.example",
		Password:        "password123",
		ConfirmPassword: "password123",
		State:           state,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	u, _ := url.Parse(redirectURL)
	if u.Query().Get("code") == "" || u.Query().Get("state") != "registerstate" {
		t.Fatalf("bad redirect_url: %s", redirectURL)
	}
	if svc.AllowsRegistration(context.Background()) {
		t.Fatal("registration should close after bootstrap user exists")
	}
	_, err = svc.Register(context.Background(), local.RegisterInput{
		Name:            "Second User",
		Email:           "second@test.example",
		Password:        "password123",
		ConfirmPassword: "password123",
		State:           state,
	})
	if !errors.Is(err, local.ErrRegistrationDisabled) {
		t.Fatalf("second register err = %v, want ErrRegistrationDisabled", err)
	}
}

func TestTokenExchangeAndIDToken(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	userID := uuid.NewString()
	users, creds := seedUserAndCreds(userID, "user@test.example", "pass")
	codeStore := newFakeCodeStore()
	tokenStore := newFakeTokenStore()

	issuer := newTestIssuer(cfg, codeStore, tokenStore, users, creds)
	rawCode, verifier := issueLoginCode(t, cfg, codeStore, users, creds, "user@test.example", "pass")

	// Step 2: token endpoint.
	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {rawCode},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code_verifier": {verifier},
	}
	tr := httptest.NewRequest(http.MethodPost, "/oidc/local/token", strings.NewReader(tokenForm.Encode()))
	tr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tw := httptest.NewRecorder()
	issuer.HandleToken(tw, tr)
	if tw.Code != http.StatusOK {
		t.Fatalf("token: status %d. body: %s", tw.Code, tw.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(tw.Body.Bytes(), &resp)

	rawAccessToken, _ := resp["access_token"].(string)
	idToken, _ := resp["id_token"].(string)
	if rawAccessToken == "" || idToken == "" {
		t.Fatalf("missing access_token or id_token in response: %v", resp)
	}

	// Verify ID token signature and claims.
	claims, err := local.VerifyES256(idToken, &key.PublicKey)
	if err != nil {
		t.Fatalf("verify id_token: %v", err)
	}
	if claims["sub"] != userID {
		t.Errorf("sub = %v, want %s", claims["sub"], userID)
	}
	if claims["email"] != "user@test.example" {
		t.Errorf("email = %v", claims["email"])
	}
	if claims["email_verified"] != true {
		t.Errorf("email_verified = %v, want true", claims["email_verified"])
	}
	if claims["iss"] != cfg.IssuerURL {
		t.Errorf("iss = %v, want %s", claims["iss"], cfg.IssuerURL)
	}
}

func TestCodeCannotBeReused(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	userID := uuid.NewString()
	users, creds := seedUserAndCreds(userID, "u@test.example", "pass")
	codeStore := newFakeCodeStore()

	issuer := newTestIssuer(cfg, codeStore, newFakeTokenStore(), users, creds)
	rawCode, verifier := issueLoginCode(t, cfg, codeStore, users, creds, "u@test.example", "pass")

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {rawCode},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code_verifier": {verifier},
	}

	// First exchange succeeds.
	tr := httptest.NewRequest(http.MethodPost, "/oidc/local/token", strings.NewReader(tokenForm.Encode()))
	tr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tw := httptest.NewRecorder()
	issuer.HandleToken(tw, tr)
	if tw.Code != http.StatusOK {
		t.Fatalf("first exchange: status %d", tw.Code)
	}

	// Second exchange with same code fails.
	tr2 := httptest.NewRequest(http.MethodPost, "/oidc/local/token", strings.NewReader(tokenForm.Encode()))
	tr2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tw2 := httptest.NewRecorder()
	issuer.HandleToken(tw2, tr2)
	if tw2.Code != http.StatusBadRequest {
		t.Errorf("reuse: status %d, want 400", tw2.Code)
	}
	var errResp map[string]string
	_ = json.Unmarshal(tw2.Body.Bytes(), &errResp)
	if errResp["error"] != "invalid_grant" {
		t.Errorf("error %q, want invalid_grant", errResp["error"])
	}
}

func TestPKCERequired(t *testing.T) {
	key := generateTestKey(t)
	cfg := publicConfig(t, key) // public client requires PKCE
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"response_type": {"code"},
		"scope":         {"openid"},
		// No code_challenge → should be rejected.
	}
	r := httptest.NewRequest(http.MethodGet, "/oidc/local/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	issuer.HandleAuthorize(w, r)

	if w.Code == http.StatusFound {
		t.Error("public client without PKCE should not redirect")
	}
}

func TestPKCEVerification(t *testing.T) {
	key := generateTestKey(t)
	cfg := publicConfig(t, key)
	userID := uuid.NewString()
	users, creds := seedUserAndCreds(userID, "u@test.example", "pass")
	codeStore := newFakeCodeStore()

	issuer := newTestIssuer(cfg, codeStore, newFakeTokenStore(), users, creds)
	rawCode, _ := issueLoginCode(t, cfg, codeStore, users, creds, "u@test.example", "pass")

	// Token with wrong verifier → rejected.
	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {rawCode},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"client_id":     {cfg.ClientID},
		"code_verifier": {"wrong-verifier"},
	}
	tr := httptest.NewRequest(http.MethodPost, "/oidc/local/token", strings.NewReader(tokenForm.Encode()))
	tr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tw := httptest.NewRecorder()
	issuer.HandleToken(tw, tr)
	if tw.Code != http.StatusBadRequest {
		t.Errorf("wrong verifier: status %d, want 400", tw.Code)
	}
}

func TestUserinfoWithValidToken(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	userID := uuid.NewString()
	users := newFakeUserStore(auth.User{ID: userID, Email: "info@test.example", Name: "Info User", IsActive: true})
	tokenStore := newFakeTokenStore()

	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), tokenStore,
		users, newFakeCredStore())

	// Seed an access token directly.
	rawToken := strings.Repeat("a", 64)
	hash := tokenHash(rawToken)
	tokenStore.tokens[hash] = auth.OIDCAccessToken{
		ID:        uuid.NewString(),
		TokenHash: hash,
		UserID:    userID,
		ClientID:  cfg.ClientID,
		Scopes:    []string{"openid", "email", "profile"},
		ExpiresAt: time.Now().Add(time.Hour),
	}

	r := httptest.NewRequest(http.MethodGet, "/oidc/local/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	issuer.HandleUserinfo(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("userinfo: status %d. body: %s", w.Code, w.Body.String())
	}
	var claims map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &claims)
	if claims["sub"] != userID {
		t.Errorf("sub = %v, want %s", claims["sub"], userID)
	}
	if claims["email"] != "info@test.example" {
		t.Errorf("email = %v", claims["email"])
	}
}

func TestUserinfoWithNoToken(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	r := httptest.NewRequest(http.MethodGet, "/oidc/local/userinfo", nil)
	w := httptest.NewRecorder()
	issuer.HandleUserinfo(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", w.Code)
	}
}

func TestWrongClientIDRejected(t *testing.T) {
	key := generateTestKey(t)
	cfg := confidentialConfig(t, key)
	issuer := newTestIssuer(cfg,
		newFakeCodeStore(), newFakeTokenStore(),
		newFakeUserStore(),
		newFakeCredStore())

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"somecode"},
		"redirect_uri":  {cfg.RedirectURIs[0]},
		"client_id":     {"wrong-client"},
		"client_secret": {cfg.ClientSecret},
	}
	r := httptest.NewRequest(http.MethodPost, "/oidc/local/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	issuer.HandleToken(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("wrong client: status %d, want 400", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	var resp map[string]string
	_ = json.Unmarshal(body, &resp)
	if resp["error"] != "invalid_client" {
		t.Errorf("error %q, want invalid_client", resp["error"])
	}
}

func TestConfigValidate(t *testing.T) {
	key := generateTestKey(t)
	cases := []struct {
		name   string
		cfg    local.Config
		wantOK bool
	}{
		{
			name:   "valid",
			cfg:    local.Config{IssuerURL: "http://h/oidc/local", ClientID: "c", RedirectURIs: []string{"http://h/cb"}, SigningKey: key},
			wantOK: true,
		},
		{
			name:   "missing issuer_url",
			cfg:    local.Config{ClientID: "c", RedirectURIs: []string{"http://h/cb"}, SigningKey: key},
			wantOK: false,
		},
		{
			name:   "missing redirect_uris",
			cfg:    local.Config{IssuerURL: "http://h/oidc/local", ClientID: "c", SigningKey: key},
			wantOK: false,
		},
		{
			name:   "missing client_id",
			cfg:    local.Config{IssuerURL: "http://h/oidc/local", RedirectURIs: []string{"http://h/cb"}, SigningKey: key},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantOK && err != nil {
				t.Errorf("want valid, got: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

func TestKeyPEMRoundTrip(t *testing.T) {
	key := generateTestKey(t)
	pemStr := keyToPEM(key)
	der, _ := x509.MarshalECPrivateKey(key)
	if string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})) != pemStr {
		t.Error("key PEM round-trip mismatch")
	}
}
