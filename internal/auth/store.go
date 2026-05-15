package auth

import (
	"context"
	"time"
)

// AuthStore provides typed access to auth-related data in the database.
// This is separate from config.Store — auth methods are NOT mixed in.
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
