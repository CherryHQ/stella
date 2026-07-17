package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
)

// ListChannelIdentities returns a target user's channel identities. Admin-only;
// the target must exist.
func (s *Service) ListChannelIdentities(ctx context.Context, authority authz.Authority, targetID string) ([]auth.ChannelIdentity, error) {
	if err := gateAdmin(authority); err != nil {
		return nil, err
	}
	if _, err := s.users.GetUser(ctx, targetID); err != nil {
		return nil, ErrUserNotFound
	}
	identities, err := s.channels.ListChannelIdentitiesByUser(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("%w: list channel identities: %w", ErrUnavailable, err)
	}
	return identities, nil
}

// DeleteUserChannelIdentity removes a target user's channel identity. Admin-only;
// the identity must exist and belong to the named target.
func (s *Service) DeleteUserChannelIdentity(ctx context.Context, authority authz.Authority, targetID, identityID string) error {
	if err := gateAdmin(authority); err != nil {
		return err
	}
	identity, err := s.channels.GetChannelIdentity(ctx, identityID)
	if err != nil {
		return ErrIdentityNotFound
	}
	if identity.UserID != targetID {
		return ErrIdentityNotOwnedByTarget
	}
	if err := s.channels.DeleteChannelIdentity(ctx, identityID); err != nil {
		return fmt.Errorf("%w: delete channel identity: %w", ErrUnavailable, err)
	}
	return nil
}

// ListLoginIdentities returns a target user's OIDC login identities. Admin-only;
// the target must exist.
func (s *Service) ListLoginIdentities(ctx context.Context, authority authz.Authority, targetID string) ([]auth.LoginIdentity, error) {
	if err := gateAdmin(authority); err != nil {
		return nil, err
	}
	if _, err := s.users.GetUser(ctx, targetID); err != nil {
		return nil, ErrUserNotFound
	}
	identities, err := s.logins.ListLoginIdentitiesByUser(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("%w: list login identities: %w", ErrUnavailable, err)
	}
	return identities, nil
}

// LinkLoginInput carries the transport-validated fields for linking a login
// identity. The transport enforces that provider/subject/email are non-empty.
type LinkLoginInput struct {
	Provider        string
	ProviderSubject string
	Email           string
	Name            string
}

// LinkLoginIdentity links an OIDC login identity to a target user. Admin-only.
// The target must exist. An identity already owned by another user is a conflict;
// an identity already owned by this user is returned unchanged (idempotent).
func (s *Service) LinkLoginIdentity(ctx context.Context, authority authz.Authority, targetID string, in LinkLoginInput) (auth.LoginIdentity, error) {
	if err := gateAdmin(authority); err != nil {
		return auth.LoginIdentity{}, err
	}
	if _, err := s.users.GetUser(ctx, targetID); err != nil {
		return auth.LoginIdentity{}, ErrUserNotFound
	}
	existing, err := s.logins.GetLoginIdentityByProvider(ctx, in.Provider, in.ProviderSubject)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return auth.LoginIdentity{}, fmt.Errorf("%w: lookup login identity: %w", ErrUnavailable, err)
	}
	if err == nil {
		if existing.UserID != targetID {
			return auth.LoginIdentity{}, ErrIdentityConflict
		}
		return existing, nil
	}
	linked, err := s.logins.CreateLoginIdentity(ctx, auth.LoginIdentity{
		ID:              uuid.Must(uuid.NewV7()).String(),
		UserID:          targetID,
		Provider:        in.Provider,
		ProviderSubject: in.ProviderSubject,
		Email:           in.Email,
		Name:            in.Name,
	})
	if err != nil {
		return auth.LoginIdentity{}, fmt.Errorf("%w: create login identity: %w", ErrUnavailable, err)
	}
	return linked, nil
}

// SelfChannelIdentities returns the current user's channel identities.
func (s *Service) SelfChannelIdentities(ctx context.Context, authority authz.Authority) ([]auth.ChannelIdentity, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	identities, err := s.channels.ListChannelIdentitiesByUser(ctx, string(authority.UserID()))
	if err != nil {
		return nil, fmt.Errorf("%w: list channel identities: %w", ErrUnavailable, err)
	}
	return identities, nil
}

// UnlinkSelfChannelIdentity removes one of the current user's channel identities.
// The identity must exist and belong to the caller.
func (s *Service) UnlinkSelfChannelIdentity(ctx context.Context, authority authz.Authority, identityID string) error {
	if !authority.Valid() {
		return ErrForbidden
	}
	identity, err := s.channels.GetChannelIdentity(ctx, identityID)
	if err != nil {
		return ErrIdentityNotFound
	}
	if identity.UserID != string(authority.UserID()) {
		return ErrIdentityNotOwnedBySelf
	}
	if err := s.channels.DeleteChannelIdentity(ctx, identityID); err != nil {
		return fmt.Errorf("%w: delete channel identity: %w", ErrUnavailable, err)
	}
	return nil
}

// ListSessions returns the current user's active sessions.
func (s *Service) ListSessions(ctx context.Context, authority authz.Authority) ([]auth.Session, error) {
	if !authority.Valid() {
		return nil, ErrForbidden
	}
	sessions, err := s.sessions.ListSessionsByUser(ctx, string(authority.UserID()))
	if err != nil {
		return nil, fmt.Errorf("%w: list sessions: %w", ErrUnavailable, err)
	}
	return sessions, nil
}

// DeleteSession revokes one of the current user's sessions. A session owned by
// another user is a foreign denial (never a silent success).
func (s *Service) DeleteSession(ctx context.Context, authority authz.Authority, sessionID string) error {
	if !authority.Valid() {
		return ErrForbidden
	}
	sess, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return ErrSessionNotFound
	}
	if sess.UserID != string(authority.UserID()) {
		return ErrSessionForeign
	}
	if err := s.sessions.DeleteSession(ctx, sessionID); err != nil {
		return fmt.Errorf("%w: delete session: %w", ErrUnavailable, err)
	}
	return nil
}

// HasPassword reports whether the user has a local password credential. It is
// used by the self "me" endpoint; any lookup error is treated as "no credential"
// to match the historical presence check.
func (s *Service) HasPassword(ctx context.Context, userID string) bool {
	_, err := s.creds.GetCredentialByUserID(ctx, userID)
	return err == nil
}

// ChangePassword verifies the current password and sets a new one for the current
// user. The transport enforces the length bounds; this owns the verify-and-swap.
func (s *Service) ChangePassword(ctx context.Context, authority authz.Authority, current, next string) error {
	if !authority.Valid() {
		return ErrForbidden
	}
	userID := string(authority.UserID())
	cred, err := s.creds.GetCredentialByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("%w: get credential: %w", ErrUnavailable, err)
	}
	if err := auth.CheckPassword(cred.PasswordHash, current); err != nil {
		return ErrPasswordIncorrect
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return fmt.Errorf("%w: hash password: %w", ErrUnavailable, err)
	}
	if err := s.creds.UpdateCredentialHash(ctx, userID, hash); err != nil {
		return fmt.Errorf("%w: update password: %w", ErrUnavailable, err)
	}
	return nil
}

// LookupEmail returns a user's email for display enrichment. It is best-effort:
// the caller ignores the error and omits the field when absent.
func (s *Service) LookupEmail(ctx context.Context, userID string) (string, error) {
	u, err := s.users.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}

// LinkChannelIdentityFromLogin best-effort links a messaging-platform identity to
// a user during a login/enrollment flow. It is idempotent: an identity already
// linked to this user is left as-is, one linked to another user is logged and
// skipped, and a missing one is created. All failures are logged, never returned,
// because the identity link is a side effect of a login that has already
// succeeded.
func (s *Service) LinkChannelIdentityFromLogin(ctx context.Context, userID, platform, externalID, name string) {
	if userID == "" || platform == "" || externalID == "" {
		return
	}
	existing, err := s.channels.GetChannelIdentityByPlatform(ctx, platform, externalID)
	if err == nil {
		if existing.UserID != userID {
			s.log.Warn("channel identity already linked to another user", "platform", platform, "external_id", externalID, "login_user_id", userID, "identity_user_id", existing.UserID)
		}
		return
	}
	if !errors.Is(err, auth.ErrNotFound) && !errors.Is(err, pgx.ErrNoRows) {
		s.log.Warn("lookup channel identity failed", "platform", platform, "external_id", externalID, "user_id", userID, "error", err)
		return
	}
	if _, err := s.channels.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.Must(uuid.NewV7()).String(),
		UserID:     userID,
		Platform:   platform,
		ExternalID: externalID,
		Name:       name,
	}); err != nil {
		s.log.Warn("create channel identity failed", "platform", platform, "external_id", externalID, "user_id", userID, "error", err)
		return
	}
	s.log.Info("linked channel identity from login", "platform", platform, "external_id", externalID, "user_id", userID)
}
