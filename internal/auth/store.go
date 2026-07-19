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
	ListUsersPaged(ctx context.Context, limit, offset int64) ([]User, error)
	UpdateUser(ctx context.Context, u User) error
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)
	UpdateUserAgeKeys(ctx context.Context, userID, publicKey, privateKey string) error
	UpdateUserDefaultAgent(ctx context.Context, userID, agentID string) error
	UpdateUserNotifyIdentity(ctx context.Context, userID string, identityID *string) error
	UpdateUserRole(ctx context.Context, userID string, role string) error
	UpdateUserActive(ctx context.Context, userID string, isActive bool) error
}

// UserRoleConditionalDeactivator atomically deactivates only a standard user.
// It prevents a read-then-write role race at callers that must never deactivate
// an administrator.
type UserRoleConditionalDeactivator interface {
	DeactivateUserIfUserRole(ctx context.Context, userID string) (bool, error)
}

// LoginIdentityStore provides CRUD for auth_identity (OIDC login identities).
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

// ActiveAdminStore locks one active administrator for the enclosing transaction.
// The lock prevents an enrollment from racing the last administrator's removal
// or deactivation.
type ActiveAdminStore interface {
	LockActiveAdmin(ctx context.Context) error
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

// CredentialStore provides CRUD for auth_credential (local password hashes).
type CredentialStore interface {
	CreateCredential(ctx context.Context, c Credential) (Credential, error)
	GetCredentialByUserID(ctx context.Context, userID string) (Credential, error)
	UpdateCredentialHash(ctx context.Context, userID, passwordHash string) error
	DeleteCredential(ctx context.Context, userID string) error
}

// AuthStores groups the stores needed for a single transactional login flow.
type AuthStores struct {
	Users       UserStore
	Logins      LoginIdentityStore
	Channels    ChannelIdentityStore
	Admins      ActiveAdminStore
	Sessions    SessionStore
	Credentials CredentialStore
}

// Transactioner is an optional interface that store implementations may satisfy
// to run ProcessOIDCLogin atomically. OIDCStore implements this.
type Transactioner interface {
	BeginAuthTx(ctx context.Context) (stores AuthStores, commit func() error, rollback func(), err error)
}

// AuthStore provides access to user-agent assignments and API tokens.
type AuthStore interface {
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
