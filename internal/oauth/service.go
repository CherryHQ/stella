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
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

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
	IssueOAuthAccess(ctx context.Context, userID, clientID string, scopes []string, refreshFamilyID string, ttl time.Duration) (plaintext string, rec credential.OAuthAccessRecord, err error)
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
	return s.issueTokens(ctx, code.UserID, client, code.Scopes, "")
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
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(rec.TokenHash)) != 1 {
		return nil, oidc.ErrInvalidGrant().WithDescription("invalid refresh token")
	}
	if rec.ClientID != client.ClientID {
		return nil, oidc.ErrInvalidGrant().WithDescription("refresh token was issued to a different client")
	}
	// Reuse detection: a consumed or revoked token means the family is
	// compromised -- revoke every token in it (access + refresh) and deny.
	if rec.Consumed || rec.Revoked {
		s.revokeFamily(ctx, rec.FamilyID)
		return nil, oidc.ErrInvalidGrant().WithDescription("refresh token reuse detected; family revoked")
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
	return s.issueTokens(ctx, rec.UserID, client, scopes, rec.PublicID)
}

// issueTokens mints a new access token and a new refresh token in a family. When
// rotating, rotateFromPublicID is the presented refresh token's public id: the
// new token joins the same family and the old one is consumed pointing at it. A
// concurrent double-use loses the atomic ConsumeRefresh and revokes the family.
func (s *Service) issueTokens(ctx context.Context, userID string, client Client, scopes []string, rotateFromPublicID string) (*TokenResult, error) {
	familyID := ""
	if rotateFromPublicID != "" {
		if old, err := s.store.GetRefreshByPublicID(ctx, rotateFromPublicID); err == nil {
			familyID = old.FamilyID
		}
	}
	newRefresh, err := mintRefreshToken()
	if err != nil {
		return nil, oidc.ErrServerError().WithParent(err)
	}
	created, err := s.store.CreateRefresh(ctx, RefreshCreate{
		PublicID:  newRefresh.PublicID,
		TokenHash: newRefresh.TokenHash,
		Last4:     newRefresh.Last4,
		ClientID:  client.ClientID,
		UserID:    userID,
		Scopes:    scopes,
		FamilyID:  familyID, // empty => store assigns a new family id
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
			// reuse and revoke the whole family (including the token we just made).
			s.revokeFamily(ctx, created.FamilyID)
			return nil, oidc.ErrInvalidGrant().WithDescription("refresh token reuse detected; family revoked")
		}
	}
	accessPlain, _, err := s.issuer.IssueOAuthAccess(ctx, userID, client.ClientID, scopes, created.FamilyID, s.accessTTL)
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

// revokeFamily kills a refresh family and its access tokens. Best-effort: logged,
// not surfaced, since it runs on an already-failing path.
func (s *Service) revokeFamily(ctx context.Context, familyID string) {
	if familyID == "" {
		return
	}
	if _, err := s.store.RevokeRefreshFamily(ctx, familyID); err != nil {
		s.log.Warn("oauth: revoke refresh family failed", "error", err, "family_id", familyID)
	}
	if _, err := s.store.RevokeAccessByFamily(ctx, familyID); err != nil {
		s.log.Warn("oauth: revoke access by family failed", "error", err, "family_id", familyID)
	}
}

// ---- Client management ----

// ClientRegistration is the input for registering a client.
type ClientRegistration struct {
	Name         string
	ClientType   string
	RedirectURIs []string
	Scopes       []string
}

// RegisterClient validates and creates a client. For confidential clients the
// plaintext secret is returned once (empty for public clients).
func (s *Service) RegisterClient(ctx context.Context, ownerUserID string, reg ClientRegistration) (Client, string, error) {
	if strings.TrimSpace(reg.Name) == "" {
		return Client{}, "", fmt.Errorf("name is required")
	}
	if len(reg.RedirectURIs) == 0 {
		return Client{}, "", fmt.Errorf("at least one redirect_uri is required")
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
		RedirectURIs:     reg.RedirectURIs,
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
		return "", fmt.Errorf("client not found")
	}
	if client.IsPublic() {
		return "", fmt.Errorf("public clients have no secret")
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
		return "", fmt.Errorf("client not found")
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

// RevokeGrant revokes a user's grant to a client: every refresh token and every
// access token the user holds for that client. Idempotent.
func (s *Service) RevokeGrant(ctx context.Context, userID, clientID string) error {
	if _, err := s.store.RevokeGrantForUser(ctx, userID, clientID); err != nil {
		return err
	}
	if _, err := s.store.RevokeAccessForUserClient(ctx, userID, clientID); err != nil {
		return err
	}
	return nil
}

// randToken returns a high-entropy opaque token (used for authorization codes).
func randToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
