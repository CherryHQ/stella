// Package account is the application boundary for user-account management: the
// admin and self use cases over users, their login/channel identities, sessions,
// password credential, and agent assignments. It composes the auth typed stores
// and owns the cross-write invariants the HTTP transport used to orchestrate —
// most importantly that a role change revokes the target's sessions and a
// deactivation revokes both sessions and personal access tokens, with
// deterministic, retryable partial-failure semantics.
//
// Authorization is explicit: every use case receives a trusted authz.Authority
// (minted from a verified session, never request payload) and fails closed. Admin
// use cases require an admin authority; self-or-admin use cases treat a foreign
// target as opaquely absent (ErrUserNotFound) so existence never leaks.
package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
)

// AssignmentStore mutates and lists the user<->agent assignment relation from the
// user's side. auth.AuthStore satisfies it.
type AssignmentStore interface {
	ListUserAgentIDs(ctx context.Context, userID string) ([]string, error)
	AssignAgent(ctx context.Context, userID, agentID string) error
	RemoveAgent(ctx context.Context, userID, agentID string) error
}

// PATRevoker revokes a user's personal access tokens as part of deactivation.
// credential.Service satisfies it. It is optional: a nil revoker skips the PAT
// step (matching the historical nil-front-door behavior).
type PATRevoker interface {
	RevokeUserPATs(ctx context.Context, userID string) (int64, error)
}

// Service owns the account use cases over the composed auth stores.
type Service struct {
	users    auth.UserStore
	channels auth.ChannelIdentityStore
	logins   auth.LoginIdentityStore
	sessions auth.SessionStore
	creds    auth.CredentialStore
	assign   AssignmentStore
	pats     PATRevoker
	log      *slog.Logger
}

// NewService composes the account service from its typed stores. The composition
// root passes the single OIDC store for the first five (it implements all of
// them), the auth assignment store, and the credential front door as the PAT
// revoker. log defaults to slog.Default() when nil.
func NewService(users auth.UserStore, channels auth.ChannelIdentityStore, logins auth.LoginIdentityStore, sessions auth.SessionStore, creds auth.CredentialStore, assign AssignmentStore, pats PATRevoker, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{users: users, channels: channels, logins: logins, sessions: sessions, creds: creds, assign: assign, pats: pats, log: log}
}

// Typed errors. The transport maps each to its historical HTTP status and body.
// Availability failures wrap ErrUnavailable (mapped to a logged 500).
var (
	// ErrForbidden is an authenticated denial (403).
	ErrForbidden = errors.New("account access forbidden")
	// ErrUserNotFound is the opaque "user not found" (404) used for both a missing
	// user and a foreign target a non-admin may not see.
	ErrUserNotFound = errors.New("user not found")
	// ErrUnavailable is a store/infrastructure failure (500).
	ErrUnavailable = errors.New("account store unavailable")

	// ErrInvalidRole rejects a role that is neither admin nor user (400).
	ErrInvalidRole = errors.New("role must be 'admin' or 'user'")
	// ErrSelfRoleRemoval blocks an admin from removing their own admin role (400).
	ErrSelfRoleRemoval = errors.New("cannot remove your own admin role")
	// ErrSelfDeactivate blocks an admin from deactivating their own account (400).
	ErrSelfDeactivate = errors.New("cannot deactivate your own account")

	// ErrIdentityNotFound reports a missing identity (404).
	ErrIdentityNotFound = errors.New("identity not found")
	// ErrIdentityNotOwnedByTarget reports an identity that does not belong to the
	// named target user (400, admin/notify paths).
	ErrIdentityNotOwnedByTarget = errors.New("identity does not belong to this user")
	// ErrIdentityNotOwnedBySelf reports an identity that does not belong to the
	// current user (403, self unlink path).
	ErrIdentityNotOwnedBySelf = errors.New("identity does not belong to you")
	// ErrIdentityConflict reports a login identity already owned by another user (409).
	ErrIdentityConflict = errors.New("identity is already linked to another user")

	// ErrSessionNotFound reports a missing session (404).
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionForeign reports a session owned by another user (403).
	ErrSessionForeign = errors.New("not your session")

	// ErrPasswordIncorrect reports a failed current-password check (401).
	ErrPasswordIncorrect = errors.New("current password is incorrect")
)

// AccountView is the domain value backing the auth-user resource: the user plus
// its channel identities. The transport encodes it into the API response shape.
type AccountView struct {
	User       auth.User
	Identities []auth.ChannelIdentity
}

// gateAdmin fails closed unless the authority is a valid admin.
func gateAdmin(authority authz.Authority) error {
	if !authority.Valid() || !authority.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

// gateSelfOrAdmin permits the current user or an admin; a foreign target is
// opaquely absent so existence never leaks to a non-admin.
func gateSelfOrAdmin(authority authz.Authority, targetID string) error {
	if !authority.Valid() {
		return ErrForbidden
	}
	if authority.IsAdmin() || string(authority.UserID()) == targetID {
		return nil
	}
	return ErrUserNotFound
}

// loadView loads a user and its channel identities for the resource response.
// A missing user is ErrUserNotFound; a channel-identity read failure is
// ErrUnavailable.
func (s *Service) loadView(ctx context.Context, userID string) (AccountView, error) {
	u, err := s.users.GetUser(ctx, userID)
	if err != nil {
		return AccountView{}, ErrUserNotFound
	}
	identities, err := s.channels.ListChannelIdentitiesByUser(ctx, userID)
	if err != nil {
		return AccountView{}, fmt.Errorf("%w: list identities: %w", ErrUnavailable, err)
	}
	return AccountView{User: u, Identities: identities}, nil
}

// ListUsers returns a page of account views. Admin-only. fetchLimit is the
// caller's page_size+1 (so the transport can detect a next page); offset is the
// opaque cursor offset.
func (s *Service) ListUsers(ctx context.Context, authority authz.Authority, fetchLimit, offset int64) ([]AccountView, error) {
	if err := gateAdmin(authority); err != nil {
		return nil, err
	}
	users, err := s.users.ListUsersPaged(ctx, fetchLimit, offset)
	if err != nil {
		return nil, fmt.Errorf("%w: list users: %w", ErrUnavailable, err)
	}
	views := make([]AccountView, 0, len(users))
	for _, u := range users {
		identities, err := s.channels.ListChannelIdentitiesByUser(ctx, u.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: list identities: %w", ErrUnavailable, err)
		}
		views = append(views, AccountView{User: u, Identities: identities})
	}
	return views, nil
}

// GetUser returns one account view, self-or-admin, foreign opaque.
func (s *Service) GetUser(ctx context.Context, authority authz.Authority, targetID string) (AccountView, error) {
	if err := gateSelfOrAdmin(authority, targetID); err != nil {
		return AccountView{}, err
	}
	return s.loadView(ctx, targetID)
}

// UpdateRole changes a user's role and revokes the target's sessions so the new
// role takes effect in fresh tokens. Admin-only; an admin cannot remove their own
// admin role. The role change and session revocation are deterministic: the role
// is persisted first, then sessions are revoked; a revocation failure is reported
// (not silently swallowed) so the caller can retry — both steps are idempotent.
func (s *Service) UpdateRole(ctx context.Context, authority authz.Authority, targetID, role string) (AccountView, error) {
	if err := gateAdmin(authority); err != nil {
		return AccountView{}, err
	}
	if role != auth.RoleAdmin && role != auth.RoleUser {
		return AccountView{}, ErrInvalidRole
	}
	if string(authority.UserID()) == targetID && role != auth.RoleAdmin {
		return AccountView{}, ErrSelfRoleRemoval
	}
	if err := s.users.UpdateUserRole(ctx, targetID, role); err != nil {
		return AccountView{}, fmt.Errorf("%w: update role: %w", ErrUnavailable, err)
	}
	if err := s.sessions.DeleteUserSessions(ctx, targetID); err != nil {
		return AccountView{}, fmt.Errorf("%w: revoke sessions after role change: %w", ErrUnavailable, err)
	}
	return s.loadView(ctx, targetID)
}

// SetActive toggles a user's active flag. Admin-only; an admin cannot deactivate
// their own account. Deactivation is a deterministic lockdown: after the flag is
// persisted, both the sessions and the PATs are revoked. Each revocation is
// attempted even if the other fails, and any failure is reported so the caller
// retries — every step is idempotent, so the account converges to fully locked
// down. Reactivation performs no revocation.
func (s *Service) SetActive(ctx context.Context, authority authz.Authority, targetID string, active bool) (AccountView, error) {
	if err := gateAdmin(authority); err != nil {
		return AccountView{}, err
	}
	if string(authority.UserID()) == targetID && !active {
		return AccountView{}, ErrSelfDeactivate
	}
	if _, err := s.users.GetUser(ctx, targetID); err != nil {
		return AccountView{}, ErrUserNotFound
	}
	if err := s.users.UpdateUserActive(ctx, targetID, active); err != nil {
		return AccountView{}, fmt.Errorf("%w: update active: %w", ErrUnavailable, err)
	}
	if !active {
		if err := s.lockDown(ctx, targetID); err != nil {
			return AccountView{}, err
		}
	}
	return s.loadView(ctx, targetID)
}

// DeactivateUserIfUserRole is the narrow conditional lifecycle operation for
// integrations that may deactivate ordinary accounts but must never deactivate
// an administrator. Its UPDATE is the linearization point against promotion;
// lockdown only follows a successful conditional write.
func (s *Service) DeactivateUserIfUserRole(ctx context.Context, authority authz.Authority, targetID string) (AccountView, error) {
	if err := gateAdmin(authority); err != nil {
		return AccountView{}, err
	}
	if string(authority.UserID()) == targetID {
		return AccountView{}, ErrSelfDeactivate
	}
	conditional, ok := s.users.(auth.UserRoleConditionalDeactivator)
	if !ok {
		return AccountView{}, fmt.Errorf("%w: conditional deactivation is unsupported", ErrUnavailable)
	}
	updated, err := conditional.DeactivateUserIfUserRole(ctx, targetID)
	if err != nil {
		return AccountView{}, fmt.Errorf("%w: conditionally deactivate user: %w", ErrUnavailable, err)
	}
	if !updated {
		view, err := s.loadView(ctx, targetID)
		if err != nil {
			return AccountView{}, err
		}
		if view.User.Role != auth.RoleUser {
			return AccountView{}, ErrForbidden
		}
		return AccountView{}, fmt.Errorf("%w: conditional deactivation did not update user", ErrUnavailable)
	}
	if err := s.lockDown(ctx, targetID); err != nil {
		return AccountView{}, err
	}
	return s.loadView(ctx, targetID)
}

func (s *Service) lockDown(ctx context.Context, targetID string) error {
	var errs []error
	if err := s.sessions.DeleteUserSessions(ctx, targetID); err != nil {
		errs = append(errs, fmt.Errorf("revoke sessions: %w", err))
	}
	if s.pats != nil {
		if _, err := s.pats.RevokeUserPATs(ctx, targetID); err != nil {
			errs = append(errs, fmt.Errorf("revoke PATs: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: lock down deactivated account: %w", ErrUnavailable, errors.Join(errs...))
	}
	return nil
}

// ListUserAgents returns the agent IDs assigned to a user. Admin-only.
func (s *Service) ListUserAgents(ctx context.Context, authority authz.Authority, targetID string) ([]string, error) {
	if err := gateAdmin(authority); err != nil {
		return nil, err
	}
	ids, err := s.assign.ListUserAgentIDs(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("%w: list user agents: %w", ErrUnavailable, err)
	}
	return ids, nil
}

// SetUserAgents reconciles a user's agent assignments to the desired set and
// returns the resulting list. Admin-only; the target must exist. Individual
// add/remove failures are logged and do not abort the reconciliation (matching
// the historical best-effort diff), and the returned list reflects the durable
// end state.
func (s *Service) SetUserAgents(ctx context.Context, authority authz.Authority, targetID string, desired []string) ([]string, error) {
	if err := gateAdmin(authority); err != nil {
		return nil, err
	}
	if _, err := s.users.GetUser(ctx, targetID); err != nil {
		return nil, ErrUserNotFound
	}
	current, err := s.assign.ListUserAgentIDs(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("%w: list user agents: %w", ErrUnavailable, err)
	}
	currentSet := make(map[string]bool, len(current))
	for _, id := range current {
		currentSet[id] = true
	}
	desiredSet := make(map[string]bool, len(desired))
	for _, id := range desired {
		desiredSet[id] = true
	}
	for _, id := range current {
		if !desiredSet[id] {
			if err := s.assign.RemoveAgent(ctx, targetID, id); err != nil {
				s.log.Error("remove agent assignment", "user_id", targetID, "agent_id", id, "error", err)
			}
		}
	}
	for _, id := range desired {
		if !currentSet[id] {
			if err := s.assign.AssignAgent(ctx, targetID, id); err != nil {
				s.log.Error("assign agent", "user_id", targetID, "agent_id", id, "error", err)
			}
		}
	}
	updated, err := s.assign.ListUserAgentIDs(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("%w: list user agents: %w", ErrUnavailable, err)
	}
	return updated, nil
}

// SetDefaultAgent sets a user's default agent. Self-or-admin, foreign opaque. The
// caller (transport) is responsible for authorizing the target user's access to
// the agent through the Agent PEP before calling — that cross-domain decision
// stays with the Agent domain.
func (s *Service) SetDefaultAgent(ctx context.Context, authority authz.Authority, targetID, agentID string) (AccountView, error) {
	if err := gateSelfOrAdmin(authority, targetID); err != nil {
		return AccountView{}, err
	}
	if err := s.users.UpdateUserDefaultAgent(ctx, targetID, agentID); err != nil {
		return AccountView{}, fmt.Errorf("%w: update default agent: %w", ErrUnavailable, err)
	}
	return s.loadView(ctx, targetID)
}

// SetNotifyIdentity sets a user's notification channel identity. Self-or-admin,
// foreign opaque. A non-nil identity must exist and belong to the target user.
func (s *Service) SetNotifyIdentity(ctx context.Context, authority authz.Authority, targetID string, identityID *string) (AccountView, error) {
	if err := gateSelfOrAdmin(authority, targetID); err != nil {
		return AccountView{}, err
	}
	if identityID != nil {
		identity, err := s.channels.GetChannelIdentity(ctx, *identityID)
		if err != nil {
			return AccountView{}, ErrIdentityNotFound
		}
		if identity.UserID != targetID {
			return AccountView{}, ErrIdentityNotOwnedByTarget
		}
	}
	if err := s.users.UpdateUserNotifyIdentity(ctx, targetID, identityID); err != nil {
		return AccountView{}, fmt.Errorf("%w: update notify identity: %w", ErrUnavailable, err)
	}
	return s.loadView(ctx, targetID)
}
