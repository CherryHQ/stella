package auth

import (
	"context"
	"time"
)

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
type LoginIdentityStore interface {
	CreateLoginIdentity(ctx context.Context, i LoginIdentity) (LoginIdentity, error)
	GetLoginIdentityByProvider(ctx context.Context, provider, providerSubject string) (LoginIdentity, error)
	ListLoginIdentitiesByUser(ctx context.Context, userID string) ([]LoginIdentity, error)
	UpdateLoginIdentity(ctx context.Context, i LoginIdentity) error
}

// ChannelIdentityStore provides CRUD for auth_channel_identity (messaging platform identities).
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
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessionsByUser(ctx context.Context, userID string) ([]Session, error)
}

// OrganizationStore provides CRUD for auth_organization.
type OrganizationStore interface {
	CreateOrganization(ctx context.Context, o Organization) (Organization, error)
	GetOrganization(ctx context.Context, id string) (Organization, error)
	GetOrganizationBySource(ctx context.Context, source, externalID string) (Organization, error)
	ListOrganizations(ctx context.Context) ([]Organization, error)
	UpdateOrganizationName(ctx context.Context, id, name string) error
	DeleteOrganization(ctx context.Context, id string) error
}

// MembershipStore provides CRUD for auth_membership.
type MembershipStore interface {
	CreateMembership(ctx context.Context, m Membership) (Membership, error)
	GetMembership(ctx context.Context, userID, orgID string) (Membership, error)
	GetUserMembership(ctx context.Context, userID string) (Membership, error)
	UpdateMembershipRole(ctx context.Context, id, role string) error
	UpdateMembershipActive(ctx context.Context, id string, active bool) error
	DeleteMembership(ctx context.Context, id string) error
	CountOrgMembers(ctx context.Context, orgID string) (int64, error)
}

// InviteStore provides CRUD for auth_invite (organization invitations).
type InviteStore interface {
	CreateInvite(ctx context.Context, inv Invite) (Invite, error)
	GetInviteByTokenHash(ctx context.Context, tokenHash string) (Invite, error)
	ListInvitesByOrg(ctx context.Context, orgID string) ([]Invite, error)
	ConsumeInvite(ctx context.Context, id string, acceptedBy string) error
	RevokeInvite(ctx context.Context, id string) error
}

// OIDCCodeStore provides operations for local OIDC authorization codes.
type OIDCCodeStore interface {
	CreateOIDCCode(ctx context.Context, c OIDCCode) (OIDCCode, error)
	// ConsumeOIDCCode atomically looks up a code by hash and marks it consumed.
	// Returns ErrNotFound if the code does not exist, ErrAlreadyConsumed if already used.
	// ConsumeOIDCCode atomically looks up a code by hash and marks it consumed.
	// Returns ErrNotFound if the code does not exist, ErrAlreadyConsumed if already used.
	ConsumeOIDCCode(ctx context.Context, codeHash string) (OIDCCode, error)
}

// OIDCAccessTokenStore provides operations for local OIDC access tokens.
type OIDCAccessTokenStore interface {
	CreateOIDCAccessToken(ctx context.Context, t OIDCAccessToken) (OIDCAccessToken, error)
	GetOIDCAccessTokenByHash(ctx context.Context, tokenHash string) (OIDCAccessToken, error)
	DeleteExpiredOIDCAccessTokens(ctx context.Context) error
}

// CredentialStore provides CRUD for auth_credential (local password hashes).
type CredentialStore interface {
	CreateCredential(ctx context.Context, c Credential) (Credential, error)
	GetCredentialByUserID(ctx context.Context, userID string) (Credential, error)
	UpdateCredentialHash(ctx context.Context, userID, passwordHash string) error
	DeleteCredential(ctx context.Context, userID string) error
}

// AuthStores groups the stores needed for a single transactional login flow.
type AuthStores struct {
	Users         UserStore
	Logins        LoginIdentityStore
	Sessions      SessionStore
	Organizations OrganizationStore
	Memberships   MembershipStore
	Credentials   CredentialStore
	Invites       InviteStore
}

// Transactioner is an optional interface that store implementations may satisfy
// to run ProcessOIDCLogin atomically. OIDCStore implements this.
type Transactioner interface {
	BeginAuthTx(ctx context.Context) (stores AuthStores, commit func() error, rollback func(), err error)
}

// AuthStore provides access to policies, user-agent assignments, and API tokens.
type AuthStore interface {
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

	// API tokens (auth_user_token)
	CreateUserToken(ctx context.Context, token UserToken) (UserToken, error)
	GetUserTokenByHash(ctx context.Context, tokenHash string) (UserToken, error)
	GetActiveUserTokenByHash(ctx context.Context, tokenHash string) (UserToken, error)
	GetActiveAutoUserToken(ctx context.Context, userID string) (UserToken, error)
	RotateUserToken(ctx context.Context, id string) (int64, error)
	RevokeUserToken(ctx context.Context, id string) (int64, error)
	UpdateUserTokenLastUsed(ctx context.Context, id string) (int64, error)
}
