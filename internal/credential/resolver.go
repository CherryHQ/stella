package credential

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// PATStore is the storage backend for the personal_access_token table. It is the
// only storage the credential package touches directly; OAuth access tokens use
// OAuthAccessStore.
type PATStore interface {
	CreatePAT(ctx context.Context, rec PATRecord) (PATRecord, error)
	GetPATByPublicID(ctx context.Context, publicID string) (PATRecord, error)
	ListPATByUser(ctx context.Context, userID string) ([]PATRecord, error)
	RevokePAT(ctx context.Context, id, userID string) (int64, error)
	RevokePATByUser(ctx context.Context, userID string) (int64, error)
	TouchPATLastUsed(ctx context.Context, id string) (int64, error)
}

// UserLookup resolves a user id to an identity snapshot for a resolved PAT.
type UserLookup interface {
	LookupUser(ctx context.Context, userID string) (Identity, error)
}

// Service is the single front door: Resolve (identity) + Enforce (authz, a free
// function) + PAT lifecycle. Construct one with NewService and wire it into the
// HTTP middleware.
type Service struct {
	pats  PATStore
	oauth OAuthAccessStore
	users UserLookup
	now   func() time.Time
	log   *slog.Logger
}

// Config wires the backends a Service needs. PATs/Users may be nil (PAT auth
// disabled); OAuth may be nil until OAuth bearer support is enabled.
type Config struct {
	PATs   PATStore
	OAuth  OAuthAccessStore
	Users  UserLookup
	Now    func() time.Time
	Logger *slog.Logger
}

// NewService builds the credential front door.
func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{pats: cfg.PATs, oauth: cfg.OAuth, users: cfg.Users, now: cfg.Now, log: cfg.Logger}
}

// Resolve turns a raw Authorization header value into a Principal, dispatched by
// family prefix. Its three-way return is the security-critical contract:
//
//   - (nil, nil): no Bearer credential present. The caller SHOULD fall back to
//     cookie/session auth.
//   - (nil, err): a Bearer credential is present but invalid, unknown, or
//     reserved. The caller MUST deny and MUST NOT fall back to any full-access
//     path.
//   - (principal, nil): success.
func (s *Service) Resolve(ctx context.Context, header string) (*Principal, error) {
	fields := strings.Fields(header)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "Bearer") {
		return nil, nil
	}
	if len(fields) != 2 {
		return nil, fmt.Errorf("credential: invalid bearer token")
	}
	raw := fields[1]

	switch {
	case strings.HasPrefix(raw, PATPrefix):
		return s.resolvePAT(ctx, raw)
	case strings.HasPrefix(raw, OAuthAccessPrefix):
		// Opaque OAuth2 access token. Resolved from oauth_access_token storage --
		// NEVER JWKS-validated as a JWT. This is the non-negotiable guardrail: the
		// /api boundary accepts identity only from this front door.
		return s.resolveOAuth(ctx, raw)
	case strings.HasPrefix(raw, OAuthRefreshPrefix):
		// Refresh tokens are valid only at the token endpoint, never the API.
		return nil, fmt.Errorf("refresh tokens are not valid at the API boundary")
	default:
		return nil, fmt.Errorf("credential: unsupported bearer token")
	}
}

// CreatePAT mints an opaque token for its owner's current authority and persists
// it. The plaintext is returned exactly once and is never recoverable afterwards.
func (s *Service) CreatePAT(ctx context.Context, userID, name string, expiresAt *time.Time) (plaintext string, rec PATRecord, err error) {
	if s.pats == nil {
		return "", PATRecord{}, fmt.Errorf("credential: PAT store not configured")
	}
	minted, err := MintOpaque(KindPAT)
	if err != nil {
		return "", PATRecord{}, err
	}
	rec, err = s.pats.CreatePAT(ctx, PATRecord{
		PublicID:  minted.PublicID,
		UserID:    userID,
		Name:      name,
		TokenHash: minted.TokenHash,
		Last4:     minted.Last4,
		// Retain the legacy NOT NULL column without granting a second authority
		// model. Existing non-empty scope sets are ignored when resolving a PAT.
		Scopes:    []string{},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", PATRecord{}, fmt.Errorf("credential: create PAT: %w", err)
	}
	return minted.Plaintext, rec, nil
}

// ListPAT returns a user's PATs (never the secret; hashes stay in storage).
func (s *Service) ListPAT(ctx context.Context, userID string) ([]PATRecord, error) {
	if s.pats == nil {
		return nil, fmt.Errorf("credential: PAT store not configured")
	}
	return s.pats.ListPATByUser(ctx, userID)
}

// GetPAT returns one of the user's own PATs by id. The scan is over the caller's
// tokens only, so ownership is enforced before existence is revealed: a token
// owned by another user reports found=false, identical to a missing one.
// Linear over the user's list (bounded, a handful per user); add a scoped
// single-row query if that ever stops holding.
func (s *Service) GetPAT(ctx context.Context, id, userID string) (PATRecord, bool, error) {
	if s.pats == nil {
		return PATRecord{}, false, fmt.Errorf("credential: PAT store not configured")
	}
	recs, err := s.pats.ListPATByUser(ctx, userID)
	if err != nil {
		return PATRecord{}, false, err
	}
	for _, rec := range recs {
		if rec.ID == id {
			return rec, true, nil
		}
	}
	return PATRecord{}, false, nil
}

// RevokePAT revokes one of the user's own PATs. It reports whether a row was
// revoked (false = not found or already revoked).
func (s *Service) RevokePAT(ctx context.Context, id, userID string) (bool, error) {
	if s.pats == nil {
		return false, fmt.Errorf("credential: PAT store not configured")
	}
	n, err := s.pats.RevokePAT(ctx, id, userID)
	return n > 0, err
}

// RevokeUserPATs cascade-revokes every active PAT a user holds. Call it when the
// account is deactivated so tokens do not silently reactivate with the account.
// It returns the number of tokens revoked.
func (s *Service) RevokeUserPATs(ctx context.Context, userID string) (int64, error) {
	if s.pats == nil {
		return 0, fmt.Errorf("credential: PAT store not configured")
	}
	return s.pats.RevokePATByUser(ctx, userID)
}
