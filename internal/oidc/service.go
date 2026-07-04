// Package oauth is Stella's OAuth2 authorization server (issue #613). Stella
// issues scoped, revocable tokens to third-party clients acting on behalf of a
// user via authorization_code + PKCE and refresh_token. client_credentials is
// deliberately out of scope: Stella isolates on user_id and a userless token has
// no subject.
//
// Guardrail: access tokens are OPAQUE stella_oat_ credentials minted through
// credential.Service.IssueOAuthAccess and resolved through the single credential
// front door. No JWT access token is ever issued, and the protocol details that
// are error-prone (PKCE verification, token response shape, error codes) lean on
// github.com/zitadel/oidc/v3/pkg/oidc rather than being hand-derived.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/CherryHQ/stella/internal/credential"
)

// Default token lifetimes.
const (
	DefaultCodeTTL    = 60 * time.Second
	DefaultAccessTTL  = time.Hour
	DefaultRefreshTTL = 30 * 24 * time.Hour
)

// AccessIssuer is the subset of credential.Service the flow depends on: minting
// the opaque access token. Kept as an interface so the flow never reaches around
// the front door to emit its own token.
type AccessIssuer interface {
	IssueOAuthAccess(ctx context.Context, userID, clientID string, scopes []string, refreshFamilyID string, ttl time.Duration) (plaintext string, err error)
}

// Service is the authorization-server flow.
type Service struct {
	store  Store
	issuer AccessIssuer
	now    func() time.Time
	log    *slog.Logger

	codeTTL    time.Duration
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// Config wires the flow's dependencies.
type Config struct {
	Store      Store
	Issuer     AccessIssuer
	Now        func() time.Time
	Logger     *slog.Logger
	CodeTTL    time.Duration
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// NewService builds the authorization-server flow.
func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.CodeTTL == 0 {
		cfg.CodeTTL = DefaultCodeTTL
	}
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = DefaultAccessTTL
	}
	if cfg.RefreshTTL == 0 {
		cfg.RefreshTTL = DefaultRefreshTTL
	}
	return &Service{
		store: cfg.Store, issuer: cfg.Issuer, now: cfg.Now, log: cfg.Logger,
		codeTTL: cfg.CodeTTL, accessTTL: cfg.AccessTTL, refreshTTL: cfg.RefreshTTL,
	}
}

// ---- Authorization endpoint ----

// AuthorizeRequest is the parsed /oauth/authorize query.
type AuthorizeRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	Scopes              []string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// AuthorizeContext is the validated result used to render the consent screen.
type AuthorizeContext struct {
	Client Client
	Scopes []string // the concrete scopes that will be granted on approval
}

// RedirectError is a flow error that should be reported to the client by
// redirecting to its redirect_uri with error/error_description (RFC 6749 4.1.2.1),
// as opposed to a validation error shown directly to the user (bad client /
// redirect_uri, where redirecting would be unsafe).
type RedirectError struct {
	RedirectURI string
	State       string
	Code        string // oauth error code, e.g. invalid_scope
	Description string
}

func (e *RedirectError) Error() string { return e.Code + ": " + e.Description }

// Authorize validates an authorization request. A returned *RedirectError means
// the caller should redirect to the client with the error; any other error is a
// hard validation failure to render as a page (never redirect to an unvalidated
// URI).
func (s *Service) Authorize(ctx context.Context, req AuthorizeRequest) (*AuthorizeContext, error) {
	client, err := s.store.GetClient(ctx, req.ClientID)
	if err != nil {
		return nil, fmt.Errorf("unknown client")
	}
	if client.Disabled {
		return nil, fmt.Errorf("client is disabled")
	}
	if req.RedirectURI == "" || !slices.Contains(client.RedirectURIs, req.RedirectURI) {
		return nil, fmt.Errorf("redirect_uri is not registered for this client")
	}
	// From here redirect_uri is trusted, so protocol errors redirect back.
	if req.ResponseType != string(oidc.ResponseTypeCode) {
		return nil, &RedirectError{req.RedirectURI, req.State, "unsupported_response_type", "only response_type=code is supported"}
	}
	if client.IsPublic() {
		if req.CodeChallenge == "" {
			return nil, &RedirectError{req.RedirectURI, req.State, string(oidc.InvalidRequest), "code_challenge is required for public clients"}
		}
	}
	if req.CodeChallenge != "" && req.CodeChallengeMethod != string(oidc.CodeChallengeMethodS256) {
		return nil, &RedirectError{req.RedirectURI, req.State, string(oidc.InvalidRequest), "only code_challenge_method=S256 is supported"}
	}

	// Requested scopes default to the client's full allowed set.
	requested := req.Scopes
	if len(requested) == 0 {
		requested = client.Scopes
	}
	// Subset chain: requested <= client allowed <= OAuth-grantable (user perms).
	if bad, ok := credential.ValidateOAuthScopes(requested); !ok {
		return nil, &RedirectError{req.RedirectURI, req.State, string(oidc.InvalidScope), "scope not grantable: " + bad}
	}
	if bad, ok := credential.ScopesSubset(requested, client.Scopes); !ok {
		return nil, &RedirectError{req.RedirectURI, req.State, string(oidc.InvalidScope), "scope not allowed for client: " + bad}
	}
	if len(requested) == 0 {
		return nil, &RedirectError{req.RedirectURI, req.State, string(oidc.InvalidScope), "at least one scope is required"}
	}
	return &AuthorizeContext{Client: client, Scopes: requested}, nil
}

// IssueCode creates a single-use authorization code after the user consents. The
// plaintext code is returned to redirect to the client; only its hash is stored.
func (s *Service) IssueCode(ctx context.Context, userID string, req AuthorizeRequest, grantedScopes []string) (string, error) {
	code, err := randToken()
	if err != nil {
		return "", err
	}
	err = s.store.CreateCode(ctx, AuthCodeCreate{
		CodeHash:            hashCode(code),
		ClientID:            req.ClientID,
		UserID:              userID,
		RedirectURI:         req.RedirectURI,
		Scopes:              grantedScopes,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		ExpiresAt:           s.now().UTC().Add(s.codeTTL),
	})
	if err != nil {
		return "", fmt.Errorf("oauth: persist code: %w", err)
	}
	return code, nil
}

// ---- Token endpoint ----

// TokenRequest is the parsed /oauth/token form.
type TokenRequest struct {
	GrantType    string
	ClientID     string
	ClientSecret string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Scope        []string
}

// TokenResult is the successful token response plus the plaintext tokens.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	Scopes       []string
}

// Exchange handles grant_type=authorization_code and refresh_token. It returns
// an *oidc.Error on protocol failure so the handler can serialize a spec error.
func (s *Service) Exchange(ctx context.Context, req TokenRequest) (*TokenResult, error) {
	switch req.GrantType {
	case string(oidc.GrantTypeCode):
		return s.exchangeCode(ctx, req)
	case string(oidc.GrantTypeRefreshToken):
		return s.exchangeRefresh(ctx, req)
	default:
		return nil, oidc.ErrUnsupportedGrantType().WithDescription("only authorization_code and refresh_token are supported")
	}
}

// authenticateClient authenticates the token-endpoint client. Confidential
// clients present client_secret (bcrypt); public clients are identified by
// client_id and rely on PKCE binding of the code.
func (s *Service) authenticateClient(ctx context.Context, clientID, clientSecret string) (Client, error) {
	client, err := s.store.GetClient(ctx, clientID)
	if err != nil {
		return Client{}, oidc.ErrInvalidClient().WithDescription("unknown client")
	}
	if client.Disabled {
		return Client{}, oidc.ErrInvalidClient().WithDescription("client is disabled")
	}
	if client.IsPublic() {
		return client, nil
	}
	if clientSecret == "" || !verifyClientSecret(client.ClientSecretHash, clientSecret) {
		return Client{}, oidc.ErrInvalidClient().WithDescription("invalid client credentials")
	}
	return client, nil
}

func (s *Service) exchangeCode(ctx context.Context, req TokenRequest) (*TokenResult, error) {
	client, err := s.authenticateClient(ctx, req.ClientID, req.ClientSecret)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(client.GrantTypes, string(oidc.GrantTypeCode)) {
		return nil, oidc.ErrUnauthorizedClient().WithDescription("client may not use authorization_code")
	}
	// Single-use consume (atomic). Not found => unknown or replayed code.
	code, found, err := s.store.ConsumeCode(ctx, hashCode(req.Code))
	if err != nil {
		return nil, oidc.ErrServerError().WithParent(err)
	}
	if !found {
		return nil, oidc.ErrInvalidGrant().WithDescription("authorization code is invalid or already used")
	}
	if code.ClientID != client.ClientID {
		return nil, oidc.ErrInvalidGrant().WithDescription("code was issued to a different client")
	}
	if !s.now().UTC().Before(code.ExpiresAt) {
		return nil, oidc.ErrInvalidGrant().WithDescription("authorization code has expired")
	}
	if req.RedirectURI != code.RedirectURI {
		return nil, oidc.ErrInvalidGrant().WithDescription("redirect_uri does not match")
	}
	// PKCE: verify the code_verifier against the stored challenge.
	if code.CodeChallenge != "" {
		if !oidc.VerifyCodeChallenge(&oidc.CodeChallenge{
			Challenge: code.CodeChallenge,
			Method:    oidc.CodeChallengeMethod(code.CodeChallengeMethod),
		}, req.CodeVerifier) {
			return nil, oidc.ErrInvalidGrant().WithDescription("PKCE verification failed")
		}
	} else if req.CodeVerifier != "" {
		return nil, oidc.ErrInvalidGrant().WithDescription("no PKCE challenge was registered")
	}
	// New grant: familyID "" opens a fresh family; nothing to rotate from.
	return s.issueTokens(ctx, code.UserID, client, code.Scopes, "", "")
}

func (s *Service) exchangeRefresh(ctx context.Context, req TokenRequest) (*TokenResult, error) {
	client, err := s.authenticateClient(ctx, req.ClientID, req.ClientSecret)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(client.GrantTypes, string(oidc.GrantTypeRefreshToken)) {
		return nil, oidc.ErrUnauthorizedClient().WithDescription("client may not use refresh_token")
	}
	publicID, secret, err := parseRefreshToken(req.RefreshToken)
	if err != nil {
		return nil, oidc.ErrInvalidGrant().WithDescription("malformed refresh token")
	}
	rec, err := s.store.GetRefreshByPublicID(ctx, publicID)
	if err != nil {
		return nil, oidc.ErrInvalidGrant().WithDescription("unknown refresh token")
	}
	if subtle.ConstantTimeCompare([]byte(credential.HashSecret(secret)), []byte(rec.TokenHash)) != 1 {
		return nil, oidc.ErrInvalidGrant().WithDescription("invalid refresh token")
	}
	// Reuse detection runs before the client-binding check: a token whose secret
	// is valid but is already consumed or whose family is revoked signals a
	// compromised family, so revoke it regardless of which authenticated client
	// presents it.
	if rec.Consumed || rec.FamilyRevoked {
		s.revokeFamily(ctx, rec.FamilyID)
		return nil, oidc.ErrInvalidGrant().WithDescription("refresh token reuse detected; family revoked")
	}
	if rec.ClientID != client.ClientID {
		return nil, oidc.ErrInvalidGrant().WithDescription("refresh token was issued to a different client")
	}
	if !s.now().UTC().Before(rec.ExpiresAt) {
		return nil, oidc.ErrInvalidGrant().WithDescription("refresh token expired")
	}
	// Rotation may narrow scope (RFC 6749 6) but never widen it.
	scopes := rec.Scopes
	if len(req.Scope) > 0 {
		if bad, ok := credential.ScopesSubset(req.Scope, rec.Scopes); !ok {
			return nil, oidc.ErrInvalidScope().WithDescription("requested scope exceeds grant: %s", bad)
		}
		scopes = req.Scope
	}
	return s.issueTokens(ctx, rec.UserID, client, scopes, rec.FamilyID, rec.PublicID)
}

// issueTokens mints a new access token and a new refresh token in a family.
// familyID is "" for the initial authorization_code exchange (a fresh family is
// opened) and the presented token's family when rotating. When rotating,
// rotateFromPublicID is the old token's public id: the new token joins the same
// family and the old one is atomically consumed pointing at it. A concurrent
// double-use loses ConsumeRefresh and revokes the family; because access-token
// resolution checks the family at read time, no token minted here can outlive
// that revoke, so the rotation needs no surrounding transaction.
func (s *Service) issueTokens(ctx context.Context, userID string, client Client, scopes []string, familyID, rotateFromPublicID string) (*TokenResult, error) {
	if familyID == "" {
		// Fresh authorization_code grant: open a family; nothing to rotate from.
		newFamily, err := s.store.CreateFamily(ctx, userID, client.ClientID)
		if err != nil {
			return nil, oidc.ErrServerError().WithParent(err)
		}
		familyID = newFamily
	}
	return s.mintPair(ctx, userID, client, scopes, familyID, rotateFromPublicID)
}

// mintPair creates the refresh token in familyID, consumes the rotated-from token
// if any, and mints the access token. Split from issueTokens so the family is
// resolved exactly once.
func (s *Service) mintPair(ctx context.Context, userID string, client Client, scopes []string, familyID, rotateFromPublicID string) (*TokenResult, error) {
	newRefresh, err := mintRefreshToken()
	if err != nil {
		return nil, oidc.ErrServerError().WithParent(err)
	}
	created, err := s.store.CreateRefresh(ctx, RefreshCreate{
		PublicID:  newRefresh.PublicID,
		TokenHash: newRefresh.TokenHash,
		ClientID:  client.ClientID,
		UserID:    userID,
		Scopes:    scopes,
		FamilyID:  familyID,
		ExpiresAt: s.now().UTC().Add(s.refreshTTL),
	})
	if err != nil {
		return nil, oidc.ErrServerError().WithParent(err)
	}
	if rotateFromPublicID != "" {
		_, found, err := s.store.ConsumeRefresh(ctx, rotateFromPublicID, created.ID)
		if err != nil {
			return nil, oidc.ErrServerError().WithParent(err)
		}
		if !found {
			// Lost the race: someone else already rotated this token. Treat as
			// reuse and revoke the whole family (including the token we just made,
			// which shares familyID and so is rejected at resolve time).
			s.revokeFamily(ctx, familyID)
			return nil, oidc.ErrInvalidGrant().WithDescription("refresh token reuse detected; family revoked")
		}
	}
	accessPlain, err := s.issuer.IssueOAuthAccess(ctx, userID, client.ClientID, scopes, familyID, s.accessTTL)
	if err != nil {
		return nil, oidc.ErrServerError().WithParent(err)
	}
	return &TokenResult{
		AccessToken:  accessPlain,
		RefreshToken: newRefresh.Plaintext,
		TokenType:    oidc.BearerToken,
		ExpiresIn:    int(s.accessTTL.Seconds()),
		Scopes:       scopes,
	}, nil
}

// revokeFamily kills a refresh family: a single flag that revokes every access
// and refresh token under it at resolve time. Best-effort: logged, not surfaced,
// since it runs on an already-failing path.
func (s *Service) revokeFamily(ctx context.Context, familyID string) {
	if familyID == "" {
		return
	}
	if err := s.store.RevokeFamily(ctx, familyID); err != nil {
		s.log.Warn("oauth: revoke family failed", "error", err, "family_id", familyID)
	}
}

// ---- Client management ----

var (
	ErrClientNotFound       = errors.New("oauth: client not found")
	ErrPublicClientNoSecret = errors.New("oauth: public clients have no secret")
)

// ClientRegistration is the input for registering a client.
type ClientRegistration struct {
	Name         string
	ClientType   string
	RedirectURIs []string
	Scopes       []string
}

func validateRedirectURIs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("at least one redirect_uri is required")
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, candidate := range raw {
		u, err := validateRedirectURI(candidate)
		if err != nil {
			return nil, err
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out, nil
}

func validateRedirectURI(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed != raw || strings.ContainsFunc(trimmed, unicode.IsControl) {
		return "", fmt.Errorf("redirect_uri must be a valid absolute https URI")
	}
	u, err := url.Parse(trimmed)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("redirect_uri must be a valid absolute https URI")
	}
	if u.Scheme == "https" {
		return u.String(), nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return u.String(), nil
	}
	return "", fmt.Errorf("redirect_uri must use https, except http loopback for local clients")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RegisterClient validates and creates a client. For confidential clients the
// plaintext secret is returned once (empty for public clients).
func (s *Service) RegisterClient(ctx context.Context, ownerUserID string, reg ClientRegistration) (Client, string, error) {
	if strings.TrimSpace(reg.Name) == "" {
		return Client{}, "", fmt.Errorf("name is required")
	}
	redirectURIs, err := validateRedirectURIs(reg.RedirectURIs)
	if err != nil {
		return Client{}, "", err
	}
	clientType := reg.ClientType
	if clientType == "" {
		clientType = ClientTypeConfidential
	}
	if clientType != ClientTypeConfidential && clientType != ClientTypePublic {
		return Client{}, "", fmt.Errorf("client_type must be confidential or public")
	}
	if bad, ok := credential.ValidateOAuthScopes(reg.Scopes); !ok {
		return Client{}, "", fmt.Errorf("scope %q is not grantable to an OAuth client", bad)
	}
	if len(reg.Scopes) == 0 {
		return Client{}, "", fmt.Errorf("at least one scope is required")
	}
	clientID, err := generateClientID()
	if err != nil {
		return Client{}, "", err
	}
	var secretHash, plaintext string
	if clientType == ClientTypeConfidential {
		plaintext, err = generateClientSecret()
		if err != nil {
			return Client{}, "", err
		}
		secretHash, err = hashClientSecret(plaintext)
		if err != nil {
			return Client{}, "", err
		}
	}
	client, err := s.store.CreateClient(ctx, ClientCreate{
		ClientID:         clientID,
		Name:             reg.Name,
		ClientSecretHash: secretHash,
		ClientType:       clientType,
		RedirectURIs:     redirectURIs,
		GrantTypes:       []string{string(oidc.GrantTypeCode), string(oidc.GrantTypeRefreshToken)},
		Scopes:           reg.Scopes,
		OwnerUserID:      ownerUserID,
	})
	if err != nil {
		return Client{}, "", fmt.Errorf("oauth: create client: %w", err)
	}
	return client, plaintext, nil
}

// ListClients returns the caller's registered clients.
func (s *Service) ListClients(ctx context.Context, ownerUserID string) ([]Client, error) {
	return s.store.ListClientsByOwner(ctx, ownerUserID)
}

// RotateSecret issues a fresh secret for a confidential client the caller owns.
func (s *Service) RotateSecret(ctx context.Context, ownerUserID, clientID string) (string, error) {
	client, err := s.store.GetClient(ctx, clientID)
	if err != nil || client.OwnerUserID != ownerUserID {
		return "", ErrClientNotFound
	}
	if client.IsPublic() {
		return "", ErrPublicClientNoSecret
	}
	plaintext, err := generateClientSecret()
	if err != nil {
		return "", err
	}
	hash, err := hashClientSecret(plaintext)
	if err != nil {
		return "", err
	}
	n, err := s.store.UpdateClientSecret(ctx, clientID, ownerUserID, hash)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrClientNotFound
	}
	return plaintext, nil
}

// DisableClient disables a client the caller owns. It reports whether a row
// changed (false = not found or already disabled).
func (s *Service) DisableClient(ctx context.Context, ownerUserID, clientID string) (bool, error) {
	n, err := s.store.DisableClient(ctx, clientID, ownerUserID)
	return n > 0, err
}

// ListAuthorizedApps returns the apps a user has active grants for.
func (s *Service) ListAuthorizedApps(ctx context.Context, userID string) ([]AuthorizedApp, error) {
	return s.store.ListAuthorizedApps(ctx, userID)
}

// RevokeGrant revokes a user's grant to a client: every family the user holds for
// that client (which covers all its refresh + access tokens via the resolve-time
// family check) plus any outstanding authorization codes, so an in-flight code
// cannot re-establish the grant. Idempotent.
func (s *Service) RevokeGrant(ctx context.Context, userID, clientID string) error {
	if err := s.store.RevokeFamiliesForUserClient(ctx, userID, clientID); err != nil {
		return err
	}
	return s.store.RevokeCodesForUserClient(ctx, userID, clientID)
}

// randToken returns a high-entropy opaque token (used for authorization codes).
func randToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
