package auth

import "context"

// AuthStore provides typed access to auth-related data in the database.
// This is separate from config.Store — auth methods are NOT mixed in.
type AuthStore interface {
	// Users
	CreateUser(ctx context.Context, username, passwordHash string) (AuthUser, error)
	GetUser(ctx context.Context, id int64) (AuthUser, error)
	GetUserByUsername(ctx context.Context, username string) (AuthUser, error)
	ListUsers(ctx context.Context) ([]AuthUser, error)
	UpdateUser(ctx context.Context, u AuthUser) error
	DeleteUser(ctx context.Context, id int64) error
	CountUsers(ctx context.Context) (int64, error)

	// Roles
	CreateRole(ctx context.Context, r Role) (Role, error)
	GetRole(ctx context.Context, id string) (Role, error)
	ListRoles(ctx context.Context) ([]Role, error)
	UpdateRole(ctx context.Context, r Role) error
	DeleteRole(ctx context.Context, id string) error

	// User-Role assignments
	AssignRole(ctx context.Context, userID int64, roleID string) error
	RemoveRole(ctx context.Context, userID int64, roleID string) error
	ListUserRoles(ctx context.Context, userID int64) ([]Role, error)

	// Identities (linked channel accounts)
	CreateIdentity(ctx context.Context, i Identity) (Identity, error)
	GetIdentity(ctx context.Context, id int64) (Identity, error)
	GetIdentityByPlatform(ctx context.Context, platform, externalID string) (Identity, error)
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
	UpdateSessionExpiry(ctx context.Context, id string, expiresAt string) error
}
