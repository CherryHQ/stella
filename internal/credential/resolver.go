package credential

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	pkgauth "github.com/CherryHQ/stella/pkg/auth"
)

// PATStore is the storage backend for the personal_access_token table. It is the
// only storage the credential package touches directly; every other kind
// delegates verification to TokenBackend.
type PATStore interface {
	CreatePAT(ctx context.Context, rec PATRecord) (PATRecord, error)
	GetPATByPublicID(ctx context.Context, publicID string) (PATRecord, error)
	ListPATByUser(ctx context.Context, userID string) ([]PATRecord, error)
	RevokePAT(ctx context.Context, id, userID string) (int64, error)
	TouchPATLastUsed(ctx context.Context, id string) (int64, error)
}

// UserLookup resolves a user id to an identity snapshot for a resolved PAT.
type UserLookup interface {
	LookupUser(ctx context.Context, userID string) (Identity, error)
}

// ScopedResult is what TokenBackend returns for a verified sandbox scoped token.
type ScopedResult struct {
	Identity  Identity
	AgentID   string
	SessionID string
	ProjectID string
	Scopes    []string
}

// TokenBackend delegates the two existing bearer families -- the vault-injected
// legacy STELLA_TOKEN and the sandbox scoped token -- to the existing token
// service. These sub-resolvers stay thin adapters; their verification logic is
// unchanged and intentionally not duplicated here.
type TokenBackend interface {
	AuthenticateLegacy(ctx context.Context, rawToken string) (Identity, error)
	AuthenticateScoped(ctx context.Context, rawToken string) (ScopedResult, error)
}

// Service is the single front door: Resolve (identity) + Enforce (authz, a free
// function) + PAT lifecycle. Construct one with NewService and wire it into the
// HTTP middleware.
type Service struct {
	pats   PATStore
	users  UserLookup
	tokens TokenBackend
	now    func() time.Time
	log    *slog.Logger
}

// Config wires the backends a Service needs. PATs/Users may be nil (PAT auth
// disabled); Tokens may be nil (legacy/scoped bearer auth disabled).
type Config struct {
	PATs   PATStore
	Users  UserLookup
	Tokens TokenBackend
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
	return &Service{pats: cfg.PATs, users: cfg.Users, tokens: cfg.Tokens, now: cfg.Now, log: cfg.Logger}
}

// Resolve turns a raw Authorization header value into a Principal, dispatched by
// family prefix. Its three-way return is the security-critical contract:
//
//   - (nil, nil): no Bearer credential present. The caller SHOULD fall back to
//     cookie/session auth.
//   - (nil, err): a Bearer credential is present but invalid, unknown, or
//     reserved. The caller MUST deny and MUST NOT fall back to any full-access
//     path. In particular a malformed stella_pat_/stella_oat_ token is dispatched
//     here and never reaches the legacy STELLA_TOKEN lookup.
//   - (principal, nil): success.
func (s *Service) Resolve(ctx context.Context, header string) (*Principal, error) {
	scheme, raw, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	switch {
	case strings.HasPrefix(raw, PATPrefix):
		return s.resolvePAT(ctx, raw)
	case strings.HasPrefix(raw, OAuthAccessPrefix):
		// Reserved for #613. Dispatched here so it can NEVER fall through to the
		// legacy full-access path; rejected until the OAuth2 AS lands.
		return nil, fmt.Errorf("oauth access tokens are not yet supported")
	case strings.HasPrefix(raw, OAuthRefreshPrefix):
		// Refresh tokens are valid only at the token endpoint, never the API.
		return nil, fmt.Errorf("refresh tokens are not valid at the API boundary")
	case strings.HasPrefix(raw, pkgauth.ScopedTokenPrefix):
		return s.resolveScoped(ctx, raw)
	default:
		return s.resolveLegacy(ctx, raw)
	}
}

// CreatePAT validates the requested scopes against the exposability policy,
// mints an opaque token, and persists it. The plaintext is returned exactly once
// and is never recoverable afterwards.
func (s *Service) CreatePAT(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time) (plaintext string, rec PATRecord, err error) {
	if s.pats == nil {
		return "", PATRecord{}, fmt.Errorf("credential: PAT store not configured")
	}
	if bad, ok := ValidatePATScopes(scopes); !ok {
		if bad == "" {
			return "", PATRecord{}, fmt.Errorf("credential: at least one scope is required")
		}
		return "", PATRecord{}, fmt.Errorf("credential: scope %q is not grantable to a PAT", bad)
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
		Scopes:    scopes,
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
