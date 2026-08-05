package credential

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"
)

// OAuthAccessStore is the storage backend for the oauth_access_token table. Like
// PATStore it is the only OAuth storage the credential package touches directly:
// the client/code/refresh tables live in internal/oidc (the zitadel/oidc-shaped
// authorization-server storage). Keeping access-token resolution here is the
// anti-scatter guarantee -- every /api credential resolves through one front door.
type OAuthAccessStore interface {
	CreateOAuthAccess(ctx context.Context, rec OAuthAccessRecord) (OAuthAccessRecord, error)
	GetOAuthAccessByPublicID(ctx context.Context, publicID string) (OAuthAccessRecord, error)
	TouchOAuthAccessLastUsed(ctx context.Context, id string) (int64, error)
}

// resolveOAuth is the OAuth2 access-token sub-resolver. It mirrors resolvePAT:
// parse + checksum, indexed public_id lookup, constant-time hash compare,
// expiry/revocation, active-user check, throttled last_used touch. The result is
// a Principal{Kind:oauth} that Enforce scope-checks and never treats as admin;
// ordinary user ownership remains at the handler. Crucially the token is
// OPAQUE and resolved from storage -- no JWT is ever JWKS-validated here.
func (s *Service) resolveOAuth(ctx context.Context, raw string) (*Principal, error) {
	if s.oauth == nil || s.users == nil {
		return nil, fmt.Errorf("credential: oauth auth not configured")
	}
	publicID, secret, err := parseOpaqueToken(OAuthAccessPrefix, raw)
	if err != nil {
		return nil, err
	}
	rec, err := s.oauth.GetOAuthAccessByPublicID(ctx, publicID)
	if err != nil {
		return nil, fmt.Errorf("credential: oauth token lookup: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(rec.TokenHash)) != 1 {
		return nil, fmt.Errorf("credential: oauth token secret mismatch")
	}
	now := s.now().UTC()
	// Revocation is enforced at the family, checked here at read time: an access
	// token is dead the moment its refresh family is revoked, regardless of any
	// write-time cascade. This makes reuse detection fail closed by construction.
	if rec.FamilyRevokedAt != nil {
		return nil, fmt.Errorf("credential: oauth token family revoked")
	}
	if !now.Before(rec.ExpiresAt) {
		return nil, fmt.Errorf("credential: oauth token expired")
	}
	ident, err := s.users.LookupUser(ctx, rec.UserID)
	if err != nil {
		return nil, fmt.Errorf("credential: oauth token user lookup: %w", err)
	}
	if !ident.IsActive {
		return nil, fmt.Errorf("credential: oauth token user deactivated")
	}
	if _, err := s.oauth.TouchOAuthAccessLastUsed(ctx, rec.ID); err != nil {
		s.log.Warn("credential: oauth token last_used update failed", "error", err, "token_id", rec.ID)
	}
	return &Principal{
		Kind:      KindOAuth,
		UserID:    ident.UserID,
		Scopes:    rec.Scopes,
		Username:  ident.Username,
		Email:     ident.Email,
		Name:      ident.Name,
		AvatarURL: ident.AvatarURL,
		Role:      ident.Role,
		// OAuth tokens act on behalf of a user but never carry admin. Handler
		// admin gates therefore fail closed even when the owner is an admin.
		IsAdmin: false,
	}, nil
}

// IssueOAuthAccess mints an opaque stella_oat_ access token and persists it under
// a refresh family. It is the ONLY way the authorization-server token endpoint
// obtains an access token: internal/oidc calls this instead of ever emitting a
// JWT, so the opaque-token guardrail holds by construction. The plaintext is
// returned once; the persisted record is not, since no caller uses it.
func (s *Service) IssueOAuthAccess(ctx context.Context, userID, clientID string, scopes []string, refreshFamilyID string, ttl time.Duration) (plaintext string, err error) {
	if s.oauth == nil {
		return "", fmt.Errorf("credential: oauth store not configured")
	}
	if refreshFamilyID == "" {
		return "", fmt.Errorf("credential: oauth access token requires a refresh family")
	}
	minted, err := MintOpaque(KindOAuth)
	if err != nil {
		return "", err
	}
	if _, err := s.oauth.CreateOAuthAccess(ctx, OAuthAccessRecord{
		PublicID:        minted.PublicID,
		TokenHash:       minted.TokenHash,
		Last4:           minted.Last4,
		ClientID:        clientID,
		UserID:          userID,
		Scopes:          scopes,
		RefreshFamilyID: refreshFamilyID,
		ExpiresAt:       s.now().UTC().Add(ttl),
	}); err != nil {
		return "", fmt.Errorf("credential: create oauth access token: %w", err)
	}
	return minted.Plaintext, nil
}
