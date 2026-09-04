package oauth

import (
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

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
	FlowStateCompleting FlowState = "completing"
	FlowStateAuthorized FlowState = "authorized"
	FlowStateFailed     FlowState = "failed"
	FlowStateExpired    FlowState = "expired"
)

// FlowStatus is the public view of an in-flight device-flow session.
type FlowStatus struct {
	Provider        Provider
	FlowID          string
	UserID          string
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	State           FlowState
	FlowType        string        // "device_code" or "authorization_code"
	DesiredScopes   []string      // complete per-user scope set requested by this flow
	Token           *oauth2.Token // set by DeviceCodeBroker when authorized
	PKCEVerifier    string        // PKCE code verifier; set when PKCE is enabled
	Error           string        // failure reason when State is FlowStateFailed (D5)
}

// OAuthBundle is the generic versioned vault payload for all YAML-driven
// OAuth providers. It replaces provider-specific bundles so TokenManager and
// brokers can work uniformly.
type OAuthBundle struct {
	Version          int       `json:"version"`
	ClientID         string    `json:"client_id"`
	ClientSecret     string    `json:"client_secret"`
	TokenEndpoint    string    `json:"token_endpoint,omitempty"`
	AuthStyle        int       `json:"auth_style,omitzero"`
	Resource         string    `json:"resource,omitempty"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	// DesiredScopes is the cumulative per-user scope set used for the latest
	// authorization. Older bundles omit it and fall back to provider defaults.
	DesiredScopes []string `json:"desired_scopes,omitempty"`
	// GrantedScope is the raw space-separated scope string the provider returned
	// with the token (oauth2.Token.Extra("scope")). Empty means "unknown" — a
	// pre-D3 bundle or a provider that omitted the field — not "no scopes".
	GrantedScope string `json:"granted_scope,omitempty"`
	Brand        string `json:"brand,omitempty"` // e.g. "lark" or "feishu"
}

// CredentialOwner is the complete vault ownership tuple for one OAuth bundle.
// Empty user or agent IDs are meaningful for system-scoped credentials.
type CredentialOwner struct {
	Scope   string
	UserID  string
	AgentID string
}

// BundleRef uniquely identifies one persisted OAuth bundle and its refresh
// lock. ProviderKey is a runtime identity such as "github" or
// "mcp:<registration id>"; Name is the vault entry name.
type BundleRef struct {
	ProviderKey string
	Owner       CredentialOwner
	Name        string
}

// DynamicProviderConfig carries the runtime facts required to refresh a token.
// MCP fills this from discovery and DCR; manifest providers adapt their static
// registry entry into the same shape.
type DynamicProviderConfig struct {
	ProviderKey  string
	TokenURL     string
	AuthStyle    oauth2.AuthStyle
	ClientID     string
	ClientSecret string
	Resource     string
	HTTPClient   *http.Client
}

// Vault key names for the supported providers.
const (
	VaultKeyGitHub = "GH_OAUTH"
	VaultKeyLark   = "LARK_CLI_OAUTH"
	VaultKeyFeishu = "FEISHU_CLI_OAUTH"
)
