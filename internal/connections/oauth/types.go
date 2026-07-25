package oauth

import (
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
	Token           *oauth2.Token // set by DeviceCodeBroker when authorized
	PKCEVerifier    string        // PKCE code verifier; set when PKCE is enabled
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
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	Brand            string    `json:"brand,omitempty"` // e.g. "lark" or "feishu"
	// GrantedScope is the raw space-separated scope string the provider returned
	// with the token (oauth2.Token.Extra("scope")). Empty means "unknown" — a
	// pre-D3 bundle or a provider that omitted the field — not "no scopes".
	GrantedScope string `json:"granted_scope,omitempty"`
}

// Vault key names for the supported providers.
const (
	VaultKeyGitHub = "GH_OAUTH"
	VaultKeyLark   = "LARK_CLI_OAUTH"
	VaultKeyFeishu = "FEISHU_CLI_OAUTH"
)
