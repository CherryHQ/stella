package oauth

import "time"

// Provider identifies an OAuth provider.
type Provider string

const (
	ProviderGitHub Provider = "github"
	ProviderLark   Provider = "lark"
)

// FlowState is the lifecycle state of a device-flow authorization.
type FlowState string

const (
	FlowStatePending    FlowState = "pending"
	FlowStateAuthorized FlowState = "authorized"
	FlowStateFailed     FlowState = "failed"
	FlowStateExpired    FlowState = "expired"
)

// FlowStatus is the public view of an in-flight device-flow session.
type FlowStatus struct {
	Provider        Provider
	FlowID          string
	UserID          int64
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	State           FlowState
}

// OAuthBundle is the generic versioned vault payload for all YAML-driven
// OAuth providers. It replaces provider-specific bundles so TokenManager and
// brokers can work uniformly.
type OAuthBundle struct {
	Version          int       `json:"version"`
	ClientID         string    `json:"client_id"`
	ClientSecret     string    `json:"client_secret"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
	Brand            string    `json:"brand,omitempty"` // e.g. "lark" or "feishu"
}

// Vault key names for the supported providers.
const (
	VaultKeyGitHub = "GH_OAUTH"
	VaultKeyLark   = "LARK_CLI_OAUTH"
	VaultKeyFeishu = "FEISHU_CLI_OAUTH"
)
