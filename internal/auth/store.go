package auth

import (
	"context"
	"time"
)

// --- New store interfaces (OIDC-based auth) ---

// UserStore provides CRUD for auth_user.
type UserStore interface {
	CreateUser(ctx context.Context, u User) (User, error)
	GetUser(ctx context.Context, id string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateUser(ctx context.Context, u User) error
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)
	UpdateUserAgeKeys(ctx context.Context, userID, publicKey, privateKey string) error
	UpdateUserDefaultAgent(ctx context.Context, userID, agentID string) error
	UpdateUserNotifyIdentity(ctx context.Context, userID string, identityID *string) error
}

// LoginIdentityStore provides CRUD for auth_identity (OIDC login identities).
// DeleteLoginIdentity is intentionally absent — login identities are permanent.
type LoginIdentityStore interface {
	CreateLoginIdentity(ctx context.Context, i LoginIdentity) (LoginIdentity, error)
	GetLoginIdentityByProvider(ctx context.Context, provider, providerSubject string) (LoginIdentity, error)
	ListLoginIdentitiesByUser(ctx context.Context, userID string) ([]LoginIdentity, error)
	UpdateLoginIdentity(ctx context.Context, i LoginIdentity) error
}

// ChannelIdentityStore provides CRUD for channel_identity (messaging platform identities).
type ChannelIdentityStore interface {
	CreateChannelIdentity(ctx context.Context, i ChannelIdentity) (ChannelIdentity, error)
	GetChannelIdentity(ctx context.Context, id string) (ChannelIdentity, error)
	GetChannelIdentityByPlatform(ctx context.Context, platform, externalID string) (ChannelIdentity, error)
	ListChannelIdentitiesByUser(ctx context.Context, userID string) ([]ChannelIdentity, error)
	UpdateChannelIdentityExternalID(ctx context.Context, id, externalID string) error
	DeleteChannelIdentity(ctx context.Context, id string) error
}

// SessionStore provides CRUD for auth_session (token-hash-based sessions).
type SessionStore interface {
	CreateSession(ctx context.Context, s Session) (Session, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteExpiredSessions(ctx context.Context) error
	DeleteUserSessions(ctx context.Context, userID string) error
	UpdateSessionExpiry(ctx context.Context, id string, expiresAt time.Time) error
}

// OrganizationStore provides CRUD for auth_organization.
type OrganizationStore interface {
	CreateOrganization(ctx context.Context, o Organization) (Organization, error)
	GetOrganization(ctx context.Context, id string) (Organization, error)
	GetOrganizationBySource(ctx context.Context, source, externalID string) (Organization, error)
	ListOrganizations(ctx context.Context) ([]Organization, error)
	DeleteOrganization(ctx context.Context, id string) error
}

// MembershipStore provides CRUD for auth_membership.
type MembershipStore interface {
	CreateMembership(ctx context.Context, m Membership) (Membership, error)
	GetMembership(ctx context.Context, userID, orgID string) (Membership, error)
	GetUserMembership(ctx context.Context, userID string) (Membership, error) // first version: one user, one org
	UpdateMembershipRole(ctx context.Context, id, role string) error
	UpdateMembershipActive(ctx context.Context, id string, active bool) error
	DeleteMembership(ctx context.Context, id string) error
	CountOrgMembers(ctx context.Context, orgID string) (int64, error)
}

// --- Legacy store interface (kept during additive migration) ---

// AuthStore provides typed access to auth-related data in the database.
// This is separate from config.Store — auth methods are NOT mixed in.
// Kept for the additive migration period; new OIDC auth code uses the split interfaces above.
type AuthStore interface {
	// Users
	CreateUser(ctx context.Context, username, passwordHash string) (AuthUser, error)
	GetUser(ctx context.Context, id string) (AuthUser, error)
	GetUserByUsername(ctx context.Context, username string) (AuthUser, error)
	ListUsers(ctx context.Context) ([]AuthUser, error)
	UpdateUser(ctx context.Context, u AuthUser) error
	UpdateUserRole(ctx context.Context, userID string, role string) error
	UpdateUserDefaultAgent(ctx context.Context, userID string, agentID string) error
	UpdateUserNotifyIdentity(ctx context.Context, userID string, identityID *string) error
	UpdateUserAgeKeys(ctx context.Context, userID string, publicKey, privateKey string) error
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)

	// Identities (linked channel accounts)
	CreateIdentity(ctx context.Context, i Identity) (Identity, error)
	GetIdentity(ctx context.Context, id string) (Identity, error)
	GetIdentityByPlatform(ctx context.Context, platform, externalID string) (Identity, error)
	UpdateIdentityExternalID(ctx context.Context, id string, externalID string) error
	ListIdentitiesByUser(ctx context.Context, userID string) ([]Identity, error)
	DeleteIdentity(ctx context.Context, id string) error

	// Policies
	CreatePolicy(ctx context.Context, p Policy) (Policy, error)
	GetPolicy(ctx context.Context, id string) (Policy, error)
	ListPolicies(ctx context.Context) ([]Policy, error)
	ListEnabledPolicies(ctx context.Context) ([]Policy, error)
	UpdatePolicy(ctx context.Context, p Policy) error
	DeletePolicy(ctx context.Context, id string) error

	// User-Agent assignments
	AssignAgent(ctx context.Context, userID string, agentID string) error
	RemoveAgent(ctx context.Context, userID string, agentID string) error
	ListUserAgentIDs(ctx context.Context, userID string) ([]string, error)
	ListAgentUserIDs(ctx context.Context, agentID string) ([]string, error)

	// Sessions
	CreateSession(ctx context.Context, s Session) (Session, error)
	GetSession(ctx context.Context, id string) (Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteExpiredSessions(ctx context.Context) error
	DeleteUserSessions(ctx context.Context, userID string) error
	UpdateSessionExpiry(ctx context.Context, id string, expiresAt time.Time) error

	// User tokens
	CreateUserToken(ctx context.Context, token UserToken) (UserToken, error)
	GetUserTokenByHash(ctx context.Context, tokenHash string) (UserToken, error)
	GetActiveUserTokenByHash(ctx context.Context, tokenHash string) (UserToken, error)
	GetActiveAutoUserToken(ctx context.Context, userID string) (UserToken, error)
	RotateUserToken(ctx context.Context, id string) (int64, error)
	RevokeUserToken(ctx context.Context, id string) (int64, error)
	UpdateUserTokenLastUsed(ctx context.Context, id string) (int64, error)
}
