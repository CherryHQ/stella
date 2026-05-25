package auth

import "time"

// User is the OIDC-based user record stored in auth_user.
type User struct {
	ID               string  `json:"id"`
	Email            string  `json:"email"`
	Name             string  `json:"name"`
	AvatarURL        string  `json:"avatar_url"`
	DefaultAgentID   string  `json:"default_agent_id,omitempty"`
	NotifyIdentityID *string `json:"notify_identity_id,omitempty"`
	// IsActive is always true for OIDC users; retained for gateway.go compatibility.
	IsActive      bool      `json:"is_active"`
	AgePublicKey  string    `json:"-"`
	AgePrivateKey string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// LoginIdentity is an OIDC login identity stored in auth_identity.
// It is named LoginIdentity (not Identity) to avoid a conflict with the
// pre-existing channel Identity type during the migration period.
type LoginIdentity struct {
	ID              string         `json:"id"`
	UserID          string         `json:"user_id"`
	Provider        string         `json:"provider"`
	ProviderSubject string         `json:"provider_subject"`
	Email           string         `json:"email"`
	Name            string         `json:"name"`
	AvatarURL       string         `json:"avatar_url"`
	RawClaims       map[string]any `json:"raw_claims,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// ChannelIdentity is a messaging-platform identity stored in channel_identity.
// It replaces the old Identity type for channel-linked accounts (Telegram, Slack, etc.).
type ChannelIdentity struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Platform   string    `json:"platform"`
	ExternalID string    `json:"external_id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Organization is a tenant / org stored in auth_organization.
type Organization struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ExternalID string    `json:"external_id"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Membership links a User to an Organization with a role, stored in auth_membership.
type Membership struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	OrganizationID string    `json:"organization_id"`
	Role           string    `json:"role"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Principal is the request-scoped identity injected into context by the auth
// middleware. It replaces AuthInfo. All business logic depends on Principal.
type Principal struct {
	UserID    string `json:"user_id"`
	OrgID     string `json:"org_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Role      string `json:"role"` // org-level role from membership
}

// IsAdmin returns true if the principal holds the admin role.
func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

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
	UserID   string         `json:"user_id"`
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
	Type       ResourceType   `json:"type"`
	ID         string         `json:"id"`
	OwnerID    string         `json:"owner_id"`
	OwnerOrgID string         `json:"owner_org_id"` // org that owns the resource; required for org-scoped permission checks
	Attrs      map[string]any `json:"attrs,omitempty"`
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

// Session represents an HTTP session.
// TokenHash is the SHA-256 hash of the raw token stored in the session cookie.
// Old sessions (auth_sessions table) leave TokenHash empty; new sessions
// (auth_session table) always have it set.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	TokenHash string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserToken represents an API token owned by a user.
type UserToken struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	Name          string     `json:"name"`
	TokenHash     string     `json:"-"`
	TokenPrefix   string     `json:"token_prefix"`
	AutoGenerated bool       `json:"auto_generated"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RotatedAt     *time.Time `json:"rotated_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// OIDCCode is an authorization code issued by the local OIDC issuer.
// CodeHash is the SHA-256 hash of the raw opaque code sent to the client.
type OIDCCode struct {
	ID            string     `json:"id"`
	CodeHash      string     `json:"-"`
	UserID        string     `json:"user_id"`
	OrgID         string     `json:"org_id"`
	ClientID      string     `json:"client_id"`
	RedirectURI   string     `json:"redirect_uri"`
	Scopes        []string   `json:"scopes"`
	Nonce         string     `json:"nonce"`
	PKCEChallenge string     `json:"-"`
	PKCEMethod    string     `json:"pkce_method"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// OIDCAccessToken is an opaque access token issued by the local OIDC issuer.
// TokenHash is the SHA-256 hash of the raw token sent to the client.
type OIDCAccessToken struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"-"`
	UserID    string    `json:"user_id"`
	OrgID     string    `json:"org_id"`
	ClientID  string    `json:"client_id"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Credential stores the bcrypt password hash for a local OIDC authorize screen.
// It lives in auth_credential and is keyed by user_id (one per user).
type Credential struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Invite is a pending invitation to join an organization.
type Invite struct {
	ID         string    `json:"id"`
	TokenHash  string    `json:"-"`
	OrgID      string    `json:"org_id"`
	Email      string    `json:"email,omitempty"`
	Role       string    `json:"role"`
	Status     string    `json:"status"`
	MaxUses    int       `json:"max_uses"`
	UseCount   int       `json:"use_count"`
	InvitedBy  string    `json:"invited_by"`
	AcceptedBy string    `json:"accepted_by,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Invite status constants.
const (
	InviteStatusPending  = "pending"
	InviteStatusAccepted = "accepted"
	InviteStatusRevoked  = "revoked"
)

// RoleAdmin and RoleUser are the built-in role IDs.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)
