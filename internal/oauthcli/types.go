package oauthcli

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

// GHOAuthBundle is the versioned vault payload for GitHub.
// GitHub personal access tokens from device flow don't expire by default,
// but we carry optional refresh fields for future fine-grained tokens.
type GHOAuthBundle struct {
	Version      int        `json:"version"`
	AccessToken  string     `json:"access_token"`
	TokenType    string     `json:"token_type"`
	Scope        string     `json:"scope"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RefreshToken string     `json:"refresh_token,omitempty"`
}

// LarkOAuthBundle is the versioned vault payload for Lark/Feishu.
// AppSecret is stored here (already encrypted at rest in vault) so the
// TokenManager can refresh tokens without needing the app credentials
// passed in at call time.
type LarkOAuthBundle struct {
	Version          int       `json:"version"`
	AppID            string    `json:"app_id"`
	AppSecret        string    `json:"app_secret"`
	Brand            string    `json:"brand"` // "lark" or "feishu"
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

// Vault key names for the two supported providers.
const (
	VaultKeyGitHub = "GH_OAUTH"
	VaultKeyLark   = "LARK_CLI_OAUTH"
)
