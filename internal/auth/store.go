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
	GetUser(ctx context.Context, id int64) (AuthUser, error)
	GetUserByUsername(ctx context.Context, username string) (AuthUser, error)
	ListUsers(ctx context.Context) ([]AuthUser, error)
	UpdateUser(ctx context.Context, u AuthUser) error
	UpdateUserRole(ctx context.Context, userID int64, role string) error
	UpdateUserDefaultAgent(ctx context.Context, userID int64, agentID string) error
	UpdateUserNotifyIdentity(ctx context.Context, userID int64, identityID *int64) error
	UpdateUserAgeKeys(ctx context.Context, userID int64, publicKey, privateKey string) error
	DeleteUser(ctx context.Context, id int64) error
	CountUsers(ctx context.Context) (int64, error)

	// Identities (linked channel accounts)
	CreateIdentity(ctx context.Context, i Identity) (Identity, error)
	GetIdentity(ctx context.Context, id int64) (Identity, error)
	GetIdentityByPlatform(ctx context.Context, platform, externalID string) (Identity, error)
	UpdateIdentityExternalID(ctx context.Context, id int64, externalID string) error
	ListIdentitiesByUser(ctx context.Context, userID int64) ([]Identity, error)
	DeleteIdentity(ctx context.Context, id int64) error

	// Policies
	CreatePolicy(ctx context.Context, p Policy) (Policy, error)
	GetPolicy(ctx context.Context, id string) (Policy, error)
	ListPolicies(ctx context.Context) ([]Policy, error)
	ListEnabledPolicies(ctx context.Context) ([]Policy, error)
	UpdatePolicy(ctx context.Context, p Policy) error
	DeletePolicy(ctx context.Context, id string) error

	// User-Agent assignments
	AssignAgent(ctx context.Context, userID int64, agentID string) error
	RemoveAgent(ctx context.Context, userID int64, agentID string) error
	ListUserAgentIDs(ctx context.Context, userID int64) ([]string, error)
	ListAgentUserIDs(ctx context.Context, agentID string) ([]int64, error)

	// Sessions
	CreateSession(ctx context.Context, s Session) (Session, error)
	GetSession(ctx context.Context, id string) (Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteExpiredSessions(ctx context.Context) error
	DeleteUserSessions(ctx context.Context, userID int64) error
	UpdateSessionExpiry(ctx context.Context, id string, expiresAt time.Time) error
}
