package local

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
	"net/url"
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
	credentials auth.CredentialStore,
	authSvc *auth.AuthService,
	sessionMgr *auth.SessionManager,
) *Issuer {
	return &Issuer{
		cfg:         cfg,
		codes:       codes,
		tokens:      tokens,
		users:       users,
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
			"role",
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
		if r.URL.Query().Get("mode") == "register" {
			is.handleRegisterPost(w, r, ctx, params)
		} else {
			is.handleAuthorizePost(w, r, ctx, params)
		}
		return
	}

	// GET: check for existing Stella OIDC session.
	if is.sessionMgr != nil && is.authSvc != nil {
		if rawToken, err := is.sessionMgr.GetToken(r); err == nil {
			if principal, err := is.authSvc.PrincipalFromToken(ctx, rawToken); err == nil {
				is.issueCodeAndRedirect(w, r, ctx, principal.UserID, params)
				return
			}
		}
	}

	// No session — show login or register form based on mode param.
	if r.URL.Query().Get("mode") == "register" {
		is.renderRegisterForm(w, params, "")
	} else {
		is.renderLoginForm(w, params, "")
	}
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

func isJSONRequest(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

func (is *Issuer) handleAuthorizePost(w http.ResponseWriter, r *http.Request, ctx context.Context, params *authorizeParams) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	showError := func(errMsg string) {
		if isJSONRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"error":   errMsg,
			})
			return
		}
		is.renderLoginForm(w, params, errMsg)
	}

	if email == "" || password == "" {
		showError("Email and password are required.")
		return
	}

	user, err := is.users.GetUserByEmail(ctx, email)
	if err != nil {
		showError("Invalid email or password.")
		return
	}

	if !user.IsActive {
		showError("Account is disabled.")
		return
	}

	credSvc := auth.NewCredentialService(is.credentials)
	if err := credSvc.VerifyPassword(ctx, user.ID, password); err != nil {
		showError("Invalid email or password.")
		return
	}

	is.issueCodeAndRedirect(w, r, ctx, user.ID, params)
}

func (is *Issuer) handleRegisterPost(w http.ResponseWriter, r *http.Request, ctx context.Context, params *authorizeParams) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	confirmPassword := r.FormValue("confirm_password")

	showError := func(errMsg string) {
		if isJSONRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"error":   errMsg,
			})
			return
		}
		is.renderRegisterForm(w, params, errMsg)
	}

	if name == "" || email == "" || password == "" || confirmPassword == "" {
		showError("All fields are required.")
		return
	}

	if len(password) < 8 {
		showError("Password must be at least 8 characters long.")
		return
	}

	if password != confirmPassword {
		showError("Passwords do not match.")
		return
	}

	if _, err := is.users.GetUserByEmail(ctx, email); err == nil {
		showError("An account with this email already exists.")
		return
	}

	// First registered user becomes admin.
	count, err := is.users.CountUsers(ctx)
	if err != nil {
		showError("Registration failed. Please try again.")
		return
	}
	role := auth.RoleUser
	if count == 0 {
		role = auth.RoleAdmin
	}

	newUser, err := is.users.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: email,
		Name:  name,
		Role:  role,
	})
	if err != nil {
		showError("Registration failed. Please try again.")
		return
	}

	credSvc := auth.NewCredentialService(is.credentials)
	if err := credSvc.SetPassword(ctx, newUser.ID, password); err != nil {
		_ = is.users.DeleteUser(ctx, newUser.ID)
		showError("Registration failed. Please try again.")
		return
	}

	is.issueCodeAndRedirect(w, r, ctx, newUser.ID, params)
}

func (is *Issuer) issueCodeAndRedirect(w http.ResponseWriter, r *http.Request, ctx context.Context, userID string, params *authorizeParams) {
	rawCode := generateOpaqueToken()
	codeHash := hashToken(rawCode)

	_, err := is.codes.CreateOIDCCode(ctx, auth.OIDCCode{
		ID:            uuid.NewString(),
		CodeHash:      codeHash,
		UserID:        userID,
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

	if isJSONRequest(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":      true,
			"redirect_url": redirectURL,
		})
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

const formCSS = `
@import url("https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap");

:root {
	--bg-color: #0f0f10;
	--text-color: #f0f0f0;
	--muted-color: #9b9b9b;
	--card-bg: rgba(25, 25, 26, 0.45);
	--card-border: rgba(255, 255, 255, 0.08);
	--input-bg: rgba(255, 255, 255, 0.03);
	--input-border: rgba(255, 255, 255, 0.1);
	--input-focus-border: #a855f7;
	--input-focus-shadow: rgba(168, 85, 247, 0.25);
	--primary-color: #a855f7;
	--primary-hover: #9333ea;
	--primary-text: #ffffff;
	--error-bg: rgba(239, 68, 68, 0.1);
	--error-border: rgba(239, 68, 68, 0.2);
	--error-text: #ef4444;
	--orb-1: rgba(124, 58, 237, 0.12);
	--orb-2: rgba(168, 85, 247, 0.08);
	--grid-color: rgba(255, 255, 255, 0.02);
}

@media (prefers-color-scheme: light) {
	:root {
		--bg-color: #fafafa;
		--text-color: #0f0f10;
		--muted-color: #5c5c5e;
		--card-bg: rgba(255, 255, 255, 0.7);
		--card-border: rgba(0, 0, 0, 0.08);
		--input-bg: #ffffff;
		--input-border: rgba(0, 0, 0, 0.1);
		--input-focus-border: #7c3aed;
		--input-focus-shadow: rgba(124, 58, 237, 0.15);
		--primary-color: #7c3aed;
		--primary-hover: #6d28d9;
		--primary-text: #ffffff;
		--error-bg: rgba(220, 38, 38, 0.05);
		--error-border: rgba(220, 38, 38, 0.15);
		--error-text: #dc2626;
		--orb-1: rgba(124, 58, 237, 0.06);
		--orb-2: rgba(168, 85, 247, 0.04);
		--grid-color: rgba(0, 0, 0, 0.02);
	}
}

body {
	font-family: 'Plus Jakarta Sans', 'Inter', system-ui, -apple-system, sans-serif;
	background-color: var(--bg-color);
	background-image: 
		linear-gradient(to right, var(--grid-color) 1px, transparent 1px),
		linear-gradient(to bottom, var(--grid-color) 1px, transparent 1px);
	background-size: 24px 24px;
	background-position: center;
	color: var(--text-color);
	display: flex;
	align-items: center;
	justify-content: center;
	min-height: 100vh;
	margin: 0;
	position: relative;
	overflow: hidden;
}

body::before {
	content: '';
	position: absolute;
	top: -20%;
	left: -20%;
	width: 60%;
	height: 60%;
	border-radius: 50%;
	background: radial-gradient(circle, var(--orb-1) 0%, transparent 70%);
	filter: blur(80px);
	z-index: -1;
	pointer-events: none;
}

body::after {
	content: '';
	position: absolute;
	bottom: -20%;
	right: -20%;
	width: 60%;
	height: 60%;
	border-radius: 50%;
	background: radial-gradient(circle, var(--orb-2) 0%, transparent 70%);
	filter: blur(80px);
	z-index: -1;
	pointer-events: none;
}

.card {
	background: var(--card-bg);
	backdrop-filter: blur(16px);
	-webkit-backdrop-filter: blur(16px);
	border: 1px solid var(--card-border);
	border-radius: 16px;
	box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.25);
	padding: 2.5rem 2rem;
	width: 100%;
	max-width: 340px;
	z-index: 10;
	transition: all 0.3s ease;
}

.card:hover {
	box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.3), 0 0 40px rgba(124, 58, 237, 0.05);
	border-color: rgba(124, 58, 237, 0.2);
}

.brand-header {
	text-align: center;
	margin-bottom: 2rem;
}

.logo-container {
	display: inline-flex;
	align-items: center;
	justify-content: center;
	padding: 0.75rem;
	border-radius: 12px;
	background: rgba(124, 58, 237, 0.05);
	border: 1px solid rgba(124, 58, 237, 0.1);
	margin-bottom: 1.25rem;
}

h1 {
	font-size: 1.5rem;
	font-weight: 600;
	margin: 0;
	letter-spacing: -0.025em;
	color: var(--text-color);
}

.subtitle {
	font-size: 0.875rem;
	color: var(--muted-color);
	margin-top: 0.375rem;
	margin-bottom: 0;
}

label {
	display: block;
	font-size: 0.875rem;
	font-weight: 500;
	margin-bottom: 0.375rem;
	color: var(--text-color);
	opacity: 0.9;
}

input[type=email], input[type=password], input[type=text] {
	width: 100%;
	box-sizing: border-box;
	padding: 0.75rem 1rem;
	background: var(--input-bg);
	border: 1px solid var(--input-border);
	border-radius: 8px;
	font-size: 0.95rem;
	color: var(--text-color);
	margin-bottom: 1.25rem;
	transition: all 0.2s ease;
	outline: none;
}

input[type=email]:focus, input[type=password]:focus, input[type=text]:focus {
	border-color: var(--input-focus-border);
	box-shadow: 0 0 0 3px var(--input-focus-shadow);
	background: rgba(255, 255, 255, 0.01);
}

button {
	width: 100%;
	padding: 0.75rem 1rem;
	background: var(--primary-color);
	color: var(--primary-text);
	border: none;
	border-radius: 8px;
	font-size: 0.95rem;
	font-weight: 500;
	cursor: pointer;
	transition: all 0.2s ease;
	box-shadow: 0 4px 12px rgba(124, 58, 237, 0.15);
}

button:hover {
	background: var(--primary-hover);
	transform: translateY(-1px);
	box-shadow: 0 6px 16px rgba(124, 58, 237, 0.25);
}

button:active {
	transform: translateY(0);
}

.error {
	background: var(--error-bg);
	color: var(--error-text);
	border: 1px solid var(--error-border);
	border-radius: 8px;
	padding: 0.75rem 1rem;
	margin-bottom: 1.25rem;
	font-size: 0.875rem;
	display: flex;
	align-items: center;
	gap: 0.5rem;
	animation: shake 0.4s ease-in-out;
}

@keyframes shake {
	0%, 100% { transform: translateX(0); }
	25% { transform: translateX(-4px); }
	75% { transform: translateX(4px); }
}

.switch {
	text-align: center;
	margin-top: 1.5rem;
	font-size: 0.875rem;
	color: var(--muted-color);
}

.switch a {
	color: var(--primary-color);
	text-decoration: none;
	font-weight: 500;
	transition: color 0.2s ease;
}

.switch a:hover {
	text-decoration: underline;
}
`

var loginFormTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in — Stella</title>
<style>` + formCSS + `</style>
</head>
<body>
<div class="card">
  <div class="brand-header">
    <div class="logo-container">
      <svg width="32" height="32" viewBox="0 0 1024 1024" fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <radialGradient id="logo-bg" cx="35%" cy="30%" r="90%">
            <stop offset="0%" stop-color="#18315b"/>
            <stop offset="70%" stop-color="#0d1b34"/>
            <stop offset="100%" stop-color="#091223"/>
          </radialGradient>
          <linearGradient id="logo-gold" x1="0%" y1="0%" x2="100%" y2="100%">
            <stop offset="0%" stop-color="#f2d08b"/>
            <stop offset="100%" stop-color="#c99546"/>
          </linearGradient>
        </defs>
        <circle cx="512" cy="512" r="512" fill="url(#logo-bg)"/>
        <g fill="none" stroke="url(#logo-gold)" stroke-width="52" stroke-linecap="round" stroke-linejoin="round">
          <path d="M360 735 L512 300 L664 735"/>
          <path d="M423 565 L601 565"/>
        </g>
      </svg>
    </div>
    <h1>Sign in to Stella</h1>
    <p class="subtitle">Sign in to continue</p>
  </div>
  {{if .Error}}
  <div class="error">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="12" cy="12" r="10"></circle>
      <line x1="12" y1="8" x2="12" y2="12"></line>
      <line x1="12" y1="16" x2="12.01" y2="16"></line>
    </svg>
    <span>{{.Error}}</span>
  </div>
  {{end}}
  <form method="POST" action="{{.Action}}">
    <label for="email">Email</label>
    <input type="email" id="email" name="email" autocomplete="email" required>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" autocomplete="current-password" required>
    <button type="submit">Sign in</button>
  </form>
  <div class="switch">Don't have an account? <a href="{{.RegisterURL}}">Sign up</a></div>
</div>
</body>
</html>`))

var registerFormTmpl = template.Must(template.New("register").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign up — Stella</title>
<style>` + formCSS + `</style>
</head>
<body>
<div class="card">
  <div class="brand-header">
    <div class="logo-container">
      <svg width="32" height="32" viewBox="0 0 1024 1024" fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <radialGradient id="logo-bg" cx="35%" cy="30%" r="90%">
            <stop offset="0%" stop-color="#18315b"/>
            <stop offset="70%" stop-color="#0d1b34"/>
            <stop offset="100%" stop-color="#091223"/>
          </radialGradient>
          <linearGradient id="logo-gold" x1="0%" y1="0%" x2="100%" y2="100%">
            <stop offset="0%" stop-color="#f2d08b"/>
            <stop offset="100%" stop-color="#c99546"/>
          </linearGradient>
        </defs>
        <circle cx="512" cy="512" r="512" fill="url(#logo-bg)"/>
        <g fill="none" stroke="url(#logo-gold)" stroke-width="52" stroke-linecap="round" stroke-linejoin="round">
          <path d="M360 735 L512 300 L664 735"/>
          <path d="M423 565 L601 565"/>
        </g>
      </svg>
    </div>
    <h1>Create an account</h1>
    <p class="subtitle">Register to get started</p>
  </div>
  {{if .Error}}
  <div class="error">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="12" cy="12" r="10"></circle>
      <line x1="12" y1="8" x2="12" y2="12"></line>
      <line x1="12" y1="16" x2="12.01" y2="16"></line>
    </svg>
    <span>{{.Error}}</span>
  </div>
  {{end}}
  <form method="POST" action="{{.Action}}">
    <label for="name">Name</label>
    <input type="text" id="name" name="name" autocomplete="name" required>
    <label for="email">Email</label>
    <input type="email" id="email" name="email" autocomplete="email" required>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" autocomplete="new-password" required>
    <label for="confirm_password">Confirm Password</label>
    <input type="password" id="confirm_password" name="confirm_password" autocomplete="new-password" required>
    <button type="submit">Sign up</button>
  </form>
  <div class="switch">Already have an account? <a href="{{.LoginURL}}">Sign in</a></div>
</div>
</body>
</html>`))

func (is *Issuer) renderLoginForm(w http.ResponseWriter, params *authorizeParams, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginFormTmpl.Execute(w, map[string]any{
		"Action":      buildAuthorizeAction(params, ""),
		"RegisterURL": buildAuthorizeAction(params, "register"),
		"Error":       errMsg,
	})
}

func (is *Issuer) renderRegisterForm(w http.ResponseWriter, params *authorizeParams, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = registerFormTmpl.Execute(w, map[string]any{
		"Action":   buildAuthorizeAction(params, "register"),
		"LoginURL": buildAuthorizeAction(params, ""),
		"Error":    errMsg,
	})
}

func buildAuthorizeAction(p *authorizeParams, mode string) string {
	q := url.Values{
		"client_id":     {p.clientID},
		"redirect_uri":  {p.redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(p.scopes, " ")},
	}
	if p.state != "" {
		q.Set("state", p.state)
	}
	if p.nonce != "" {
		q.Set("nonce", p.nonce)
	}
	if p.pkceChallenge != "" {
		q.Set("code_challenge", p.pkceChallenge)
		q.Set("code_challenge_method", p.pkceChallengeMethod)
	}
	if mode != "" {
		q.Set("mode", mode)
	}
	return "authorize?" + q.Encode()
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

	// Load user for claims.
	user, err := is.users.GetUser(ctx, code.UserID)
	if err != nil {
		is.tokenError(w, "server_error", "could not load user")
		return
	}
	if !user.IsActive {
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
		ClientID:  clientID,
		Scopes:    code.Scopes,
		ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		is.tokenError(w, "server_error", "could not issue access token")
		return
	}

	// Build ID token.
	idToken, err := is.buildIDToken(user, code.Scopes, code.Nonce, clientID, ttl)
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
	if !user.IsActive {
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

func (is *Issuer) buildIDToken(user auth.User, scopes []string, nonce, audience string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss":  is.cfg.IssuerURL,
		"sub":  user.ID,
		"aud":  audience,
		"iat":  now.Unix(),
		"exp":  now.Add(ttl).Unix(),
		"role": user.Role,
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
