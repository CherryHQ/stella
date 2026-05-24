package localoidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

// Issuer implements the built-in local OIDC issuer endpoints.
// It exposes discovery, JWKS, authorize, token, and userinfo under a stable
// URL prefix (recommended: /oidc/local).
type Issuer struct {
	cfg         *Config
	codes       auth.OIDCCodeStore
	tokens      auth.OIDCAccessTokenStore
	users       auth.UserStore
	memberships auth.MembershipStore
	credentials auth.CredentialStore
	// authSvc is used to validate an existing Stella OIDC session on the authorize endpoint.
	// May be nil; when nil, the authorize endpoint always shows the login form.
	authSvc    *auth.AuthService
	sessionMgr *auth.SessionManager
}

// NewIssuer creates a new local OIDC issuer.
func NewIssuer(
	cfg *Config,
	codes auth.OIDCCodeStore,
	tokens auth.OIDCAccessTokenStore,
	users auth.UserStore,
	memberships auth.MembershipStore,
	credentials auth.CredentialStore,
	authSvc *auth.AuthService,
	sessionMgr *auth.SessionManager,
) *Issuer {
	return &Issuer{
		cfg:         cfg,
		codes:       codes,
		tokens:      tokens,
		users:       users,
		memberships: memberships,
		credentials: credentials,
		authSvc:     authSvc,
		sessionMgr:  sessionMgr,
	}
}

// DiscoveryDocument is the OpenID Connect discovery document served at
// /.well-known/openid-configuration under the issuer base path.
type DiscoveryDocument struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserinfoEndpoint                  string   `json:"userinfo_endpoint"`
	JWKSUri                           string   `json:"jwks_uri"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	SubjectTypesSupported             []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported  []string `json:"id_token_signing_alg_values_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ClaimsSupported                   []string `json:"claims_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
}

// HandleDiscovery serves GET /.well-known/openid-configuration.
func (is *Issuer) HandleDiscovery(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(is.cfg.IssuerURL, "/")
	doc := DiscoveryDocument{
		Issuer:                            is.cfg.IssuerURL,
		AuthorizationEndpoint:             base + "/authorize",
		TokenEndpoint:                     base + "/token",
		UserinfoEndpoint:                  base + "/userinfo",
		JWKSUri:                           base + "/jwks.json",
		ResponseTypesSupported:            []string{"code"},
		SubjectTypesSupported:             []string{"public"},
		IDTokenSigningAlgValuesSupported:  []string{"ES256"},
		ScopesSupported:                   []string{"openid", "email", "profile"},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "none"},
		ClaimsSupported: []string{
			"sub", "iss", "aud", "iat", "exp", "nonce",
			"email", "email_verified", "name", "picture",
			"org_id", "org_name", "role",
		},
		CodeChallengeMethodsSupported: []string{"S256"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// JWK is a JSON Web Key entry in the JWKS response.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

// HandleJWKS serves GET /jwks.json.
func (is *Issuer) HandleJWKS(w http.ResponseWriter, r *http.Request) {
	pub := &is.cfg.SigningKey.PublicKey
	jwk := JWK{
		Kty: "EC",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(pub.X.Bytes()),
		Y:   base64.RawURLEncoding.EncodeToString(pub.Y.Bytes()),
		Kid: is.cfg.KeyID,
		Use: "sig",
		Alg: "ES256",
	}
	resp := map[string]any{"keys": []JWK{jwk}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleAuthorize serves GET and POST /authorize.
// GET: if a valid Stella OIDC session exists, issues a code and redirects.
//
//	Otherwise renders the local credential login form.
//
// POST: verifies credentials, then issues a code and redirects.
func (is *Issuer) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	// Validate OIDC request params (from query string on GET, also query string on POST
	// since the form POSTs back with the same query string).
	params, err := is.parseAuthorizeParams(r)
	if err != nil {
		http.Error(w, "invalid_request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	if r.Method == http.MethodPost {
		is.handleAuthorizePost(w, r, ctx, params)
		return
	}

	// GET: check for existing Stella OIDC session.
	if is.sessionMgr != nil && is.authSvc != nil {
		if rawToken, err := is.sessionMgr.GetToken(r); err == nil {
			if principal, err := is.authSvc.PrincipalFromToken(ctx, rawToken); err == nil {
				is.issueCodeAndRedirect(w, r, ctx, principal.UserID, principal.OrgID, params)
				return
			}
		}
	}

	// No session — show the credential login form.
	is.renderLoginForm(w, params, "")
}

type authorizeParams struct {
	clientID            string
	redirectURI         string
	state               string
	nonce               string
	scopes              []string
	pkceChallenge       string
	pkceChallengeMethod string
}

func (is *Issuer) parseAuthorizeParams(r *http.Request) (*authorizeParams, error) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	if clientID != is.cfg.ClientID {
		return nil, fmt.Errorf("unknown client_id")
	}
	redirectURI := q.Get("redirect_uri")
	if !is.cfg.IsRedirectURIAllowed(redirectURI) {
		return nil, fmt.Errorf("redirect_uri not allowed")
	}
	if q.Get("response_type") != "code" {
		return nil, fmt.Errorf("only response_type=code is supported")
	}
	pkceChallenge := q.Get("code_challenge")
	pkceMethod := q.Get("code_challenge_method")
	if is.cfg.IsPublicClient() {
		if pkceChallenge == "" {
			return nil, fmt.Errorf("code_challenge required for public clients")
		}
		if pkceMethod != "S256" {
			return nil, fmt.Errorf("only code_challenge_method=S256 is supported")
		}
	}
	scopes := splitTrimmed(strings.ReplaceAll(q.Get("scope"), " ", ","))
	return &authorizeParams{
		clientID:            clientID,
		redirectURI:         redirectURI,
		state:               q.Get("state"),
		nonce:               q.Get("nonce"),
		scopes:              scopes,
		pkceChallenge:       pkceChallenge,
		pkceChallengeMethod: pkceMethod,
	}, nil
}

func (is *Issuer) handleAuthorizePost(w http.ResponseWriter, r *http.Request, ctx context.Context, params *authorizeParams) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	if email == "" || password == "" {
		is.renderLoginForm(w, params, "Email and password are required.")
		return
	}

	user, err := is.users.GetUserByEmail(ctx, email)
	if err != nil {
		is.renderLoginForm(w, params, "Invalid email or password.")
		return
	}

	credSvc := auth.NewCredentialService(is.credentials)
	if err := credSvc.VerifyPassword(ctx, user.ID, password); err != nil {
		is.renderLoginForm(w, params, "Invalid email or password.")
		return
	}

	// Check membership is active.
	membership, err := is.memberships.GetUserMembership(ctx, user.ID)
	if err != nil || !membership.IsActive {
		is.renderLoginForm(w, params, "Account is disabled.")
		return
	}

	is.issueCodeAndRedirect(w, r, ctx, user.ID, membership.OrganizationID, params)
}

func (is *Issuer) issueCodeAndRedirect(w http.ResponseWriter, r *http.Request, ctx context.Context, userID, orgID string, params *authorizeParams) {
	rawCode := generateOpaqueToken()
	codeHash := hashToken(rawCode)

	_, err := is.codes.CreateOIDCCode(ctx, auth.OIDCCode{
		ID:            uuid.NewString(),
		CodeHash:      codeHash,
		UserID:        userID,
		OrgID:         orgID,
		ClientID:      params.clientID,
		RedirectURI:   params.redirectURI,
		Scopes:        params.scopes,
		Nonce:         params.nonce,
		PKCEChallenge: params.pkceChallenge,
		PKCEMethod:    params.pkceChallengeMethod,
		ExpiresAt:     time.Now().Add(time.Duration(is.cfg.AuthCodeTTL) * time.Second),
	})
	if err != nil {
		http.Error(w, "server_error: could not create authorization code", http.StatusInternalServerError)
		return
	}

	redirectURL := params.redirectURI + "?code=" + rawCode
	if params.state != "" {
		redirectURL += "&state=" + params.state
	}
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

var loginFormTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in — Stella</title>
<style>
body{font-family:system-ui,sans-serif;background:#f5f5f5;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
.card{background:#fff;border-radius:8px;box-shadow:0 2px 8px rgba(0,0,0,.12);padding:2rem;width:100%;max-width:360px}
h1{font-size:1.25rem;margin:0 0 1.5rem}
label{display:block;font-size:.875rem;margin-bottom:.25rem;color:#555}
input[type=email],input[type=password]{width:100%;box-sizing:border-box;padding:.5rem .75rem;border:1px solid #ccc;border-radius:4px;font-size:1rem;margin-bottom:1rem}
button{width:100%;padding:.625rem;background:#0070f3;color:#fff;border:none;border-radius:4px;font-size:1rem;cursor:pointer}
button:hover{background:#0051bb}
.error{background:#fef2f2;color:#dc2626;border:1px solid #fca5a5;border-radius:4px;padding:.5rem .75rem;margin-bottom:1rem;font-size:.875rem}
</style>
</head>
<body>
<div class="card">
  <h1>Sign in to Stella</h1>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  <form method="POST" action="{{.Action}}">
    <label for="email">Email</label>
    <input type="email" id="email" name="email" autocomplete="email" required>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" autocomplete="current-password" required>
    <button type="submit">Sign in</button>
  </form>
</div>
</body>
</html>`))

func (is *Issuer) renderLoginForm(w http.ResponseWriter, params *authorizeParams, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginFormTmpl.Execute(w, map[string]any{
		"Action": buildAuthorizeAction(params),
		"Error":  errMsg,
	})
}

func buildAuthorizeAction(p *authorizeParams) string {
	// POST back to authorize with the same OIDC params preserved in the query string.
	q := "?client_id=" + p.clientID +
		"&redirect_uri=" + p.redirectURI +
		"&response_type=code" +
		"&scope=" + strings.Join(p.scopes, "+")
	if p.state != "" {
		q += "&state=" + p.state
	}
	if p.nonce != "" {
		q += "&nonce=" + p.nonce
	}
	if p.pkceChallenge != "" {
		q += "&code_challenge=" + p.pkceChallenge +
			"&code_challenge_method=" + p.pkceChallengeMethod
	}
	return "authorize" + q
}

// HandleToken serves POST /token.
// Supports grant_type=authorization_code only.
func (is *Issuer) HandleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		is.tokenError(w, "invalid_request", "cannot parse form")
		return
	}

	if r.FormValue("grant_type") != "authorization_code" {
		is.tokenError(w, "unsupported_grant_type", "only authorization_code is supported")
		return
	}

	clientID, _, err := is.authenticateClient(r)
	if err != nil {
		is.tokenError(w, "invalid_client", err.Error())
		return
	}

	rawCode := r.FormValue("code")
	if rawCode == "" {
		is.tokenError(w, "invalid_request", "code is required")
		return
	}
	redirectURI := r.FormValue("redirect_uri")
	codeVerifier := r.FormValue("code_verifier")

	ctx := r.Context()
	code, err := is.codes.ConsumeOIDCCode(ctx, hashToken(rawCode))
	if errors.Is(err, auth.ErrNotFound) {
		is.tokenError(w, "invalid_grant", "unknown code")
		return
	}
	if errors.Is(err, auth.ErrAlreadyConsumed) {
		is.tokenError(w, "invalid_grant", "code already used")
		return
	}
	if errors.Is(err, auth.ErrExpired) {
		is.tokenError(w, "invalid_grant", "code expired")
		return
	}
	if err != nil {
		is.tokenError(w, "server_error", "could not consume code")
		return
	}

	// Validate code params.
	if code.ClientID != clientID {
		is.tokenError(w, "invalid_grant", "client mismatch")
		return
	}
	if code.RedirectURI != redirectURI {
		is.tokenError(w, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if code.PKCEChallenge != "" {
		if err := verifyPKCE(codeVerifier, code.PKCEChallenge, code.PKCEMethod); err != nil {
			is.tokenError(w, "invalid_grant", "PKCE verification failed")
			return
		}
	}

	// Load user and membership for claims.
	user, err := is.users.GetUser(ctx, code.UserID)
	if err != nil {
		is.tokenError(w, "server_error", "could not load user")
		return
	}
	membership, err := is.memberships.GetUserMembership(ctx, code.UserID)
	if err != nil || !membership.IsActive {
		is.tokenError(w, "access_denied", "account disabled")
		return
	}

	// Issue opaque access token.
	rawAccessToken := generateOpaqueToken()
	accessTokenHash := hashToken(rawAccessToken)
	ttl := time.Duration(is.cfg.AccessTokenTTL) * time.Second
	_, err = is.tokens.CreateOIDCAccessToken(ctx, auth.OIDCAccessToken{
		ID:        uuid.NewString(),
		TokenHash: accessTokenHash,
		UserID:    code.UserID,
		OrgID:     code.OrgID,
		ClientID:  clientID,
		Scopes:    code.Scopes,
		ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		is.tokenError(w, "server_error", "could not issue access token")
		return
	}

	// Build ID token.
	idToken, err := is.buildIDToken(user, membership, code.Scopes, code.Nonce, clientID, ttl)
	if err != nil {
		is.tokenError(w, "server_error", "could not build ID token")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": rawAccessToken,
		"token_type":   "Bearer",
		"expires_in":   int(ttl.Seconds()),
		"id_token":     idToken,
	})
}

// HandleUserinfo serves GET /userinfo.
func (is *Issuer) HandleUserinfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}

	tok, err := is.tokens.GetOIDCAccessTokenByHash(ctx, hashToken(rawToken))
	if errors.Is(err, auth.ErrNotFound) || (err == nil && time.Now().After(tok.ExpiresAt)) {
		http.Error(w, "invalid_token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "server_error", http.StatusInternalServerError)
		return
	}

	user, err := is.users.GetUser(ctx, tok.UserID)
	if err != nil {
		http.Error(w, "server_error", http.StatusInternalServerError)
		return
	}
	membership, err := is.memberships.GetUserMembership(ctx, tok.UserID)
	if err != nil || !membership.IsActive {
		http.Error(w, "access_denied", http.StatusForbidden)
		return
	}

	claims := map[string]any{"sub": user.ID}
	for _, s := range tok.Scopes {
		switch strings.ToLower(s) {
		case "email":
			claims["email"] = user.Email
			claims["email_verified"] = true
		case "profile":
			claims["name"] = user.Name
			claims["picture"] = user.AvatarURL
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(claims)
}

// --- JWT helpers ---

func (is *Issuer) buildIDToken(user auth.User, membership auth.Membership, scopes []string, nonce, audience string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss": is.cfg.IssuerURL,
		"sub": user.ID,
		"aud": audience,
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	for _, s := range scopes {
		switch strings.ToLower(s) {
		case "email":
			claims["email"] = user.Email
			claims["email_verified"] = true
		case "profile":
			claims["name"] = user.Name
			claims["picture"] = user.AvatarURL
		}
	}
	// Always include org and role in ID token for membership resolution.
	claims["org_id"] = membership.OrganizationID
	claims["role"] = membership.Role

	return signES256(is.cfg.SigningKey, is.cfg.KeyID, claims)
}

// signES256 produces a compact JWS (JWT) signed with ECDSA P-256.
func signES256(key *ecdsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	header := map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	hash := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}

	// Encode r and s as big-endian 32-byte values (P-256 curve order is 32 bytes).
	sig := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ExportPublicKeyPEM exports the public key as a PEM block (for debugging).
func ExportPublicKeyPEM(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// VerifyES256 verifies a compact JWT produced by signES256. Used in tests.
func VerifyES256(tokenStr string, pub *ecdsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT")
	}
	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if len(sigBytes) != 64 {
		return nil, fmt.Errorf("unexpected signature length %d", len(sigBytes))
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	if !ecdsa.Verify(pub, hash[:], r, s) {
		return nil, errors.New("invalid signature")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// --- PKCE ---

func verifyPKCE(verifier, challenge, method string) error {
	if method != "S256" {
		return fmt.Errorf("unsupported method %q", method)
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	if computed != challenge {
		return errors.New("challenge mismatch")
	}
	return nil
}

// --- Client authentication ---

func (is *Issuer) authenticateClient(r *http.Request) (clientID, secret string, err error) {
	// Try HTTP Basic auth first.
	if id, sec, ok := r.BasicAuth(); ok {
		clientID, secret = id, sec
	} else {
		clientID = r.FormValue("client_id")
		secret = r.FormValue("client_secret")
	}
	if clientID != is.cfg.ClientID {
		return "", "", fmt.Errorf("unknown client")
	}
	if !is.cfg.IsPublicClient() && secret != is.cfg.ClientSecret {
		return "", "", fmt.Errorf("invalid client_secret")
	}
	return clientID, secret, nil
}

// --- Token helpers ---

func generateOpaqueToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("localoidc: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (is *Issuer) tokenError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": desc,
	})
}
