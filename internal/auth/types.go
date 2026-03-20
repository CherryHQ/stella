package auth

import "time"

// AuthUser represents a system user with login credentials.
type AuthUser struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Role represents an extensible role (e.g., admin, user).
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	IsSystem    bool      `json:"is_system"`
	CreatedAt   time.Time `json:"created_at"`
}

// Policy represents an ABAC policy with JSON conditions.
type Policy struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Effect     string    `json:"effect"`
	Subjects   string    `json:"subjects"`
	Actions    string    `json:"actions"`
	Resources  string    `json:"resources"`
	Conditions string    `json:"conditions"`
	Priority   int       `json:"priority"`
	IsSystem   bool      `json:"is_system"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

// Policy effect constants.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// AccessRequest represents a request to check authorization.
type AccessRequest struct {
	Subject  Subject        `json:"subject"`
	Action   Action         `json:"action"`
	Resource Resource       `json:"resource"`
	Context  map[string]any `json:"context,omitempty"`
}

// Subject represents the entity requesting access.
type Subject struct {
	UserID   int64          `json:"user_id"`
	Roles    []string       `json:"roles"`
	AgentIDs []string       `json:"agent_ids"`
	Attrs    map[string]any `json:"attrs,omitempty"`
}

// Action is a string alias for authorization actions.
type Action string

// Action constants.
const (
	ActionRead    Action = "read"
	ActionWrite   Action = "write"
	ActionCreate  Action = "create"
	ActionDelete  Action = "delete"
	ActionExecute Action = "execute"
	ActionManage  Action = "manage"
)

// Resource represents the target of an authorization request.
type Resource struct {
	Type    ResourceType   `json:"type"`
	ID      string         `json:"id"`
	OwnerID int64          `json:"owner_id"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// ResourceType is a string alias for resource types.
type ResourceType string

// ResourceType constants.
const (
	ResourceAgent     ResourceType = "agent"
	ResourceAgentList ResourceType = "agent_list"
	ResourceProvider  ResourceType = "provider"
	ResourceChannel   ResourceType = "channel"
	ResourceSession   ResourceType = "session"
	ResourceUser      ResourceType = "user"
	ResourceUserData  ResourceType = "user_data"
	ResourceSkill     ResourceType = "skill"
	ResourceScheduler ResourceType = "scheduler"
	ResourceSetting   ResourceType = "setting"
)

// Identity represents a linked channel identity.
type Identity struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	Platform   string    `json:"platform"`
	ExternalID string    `json:"external_id"`
	Name       string    `json:"name"`
	LinkedAt   time.Time `json:"linked_at"`
}

// Session represents an HTTP session.
type Session struct {
	ID        string    `json:"id"`
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// RoleAdmin and RoleUser are the built-in role IDs.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)
