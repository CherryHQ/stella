package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// oauthFlowTTL bounds one authorization attempt; the flow row expires with it.
const oauthFlowTTL = 10 * time.Minute

// oauthExchangeTimeout bounds one token endpoint round trip (exchange or
// refresh). Refreshes happen inside tool calls, so this is deliberately tight.
const oauthExchangeTimeout = 30 * time.Second

// oauthRefreshSlop refreshes the access token this long before its expiry so
// an in-flight call never rides an expiring token.
const oauthRefreshSlop = 60 * time.Second

// OAuthBundle is the versioned vault payload for one MCP registration's OAuth
// tokens. It mirrors internal/connections/oauth.OAuthBundle in shape but is
// deliberately independent: connections are fixed YAML providers, MCP servers
// are user-created, and the two must not grow coupled fields.
type OAuthBundle struct {
	Version         int       `json:"version"`
	ClientID        string    `json:"client_id"`
	TokenEndpoint   string    `json:"token_endpoint"`
	AuthStyle       int       `json:"auth_style"`
	Resource        string    `json:"resource,omitempty"`
	AccessToken     string    `json:"access_token"`
	RefreshToken    string    `json:"refresh_token,omitempty"`
	AccessExpiresAt time.Time `json:"access_expires_at"`
	GrantedScope    string    `json:"granted_scope,omitempty"`
}

// oauthFlowConfig is the durable client + endpoint configuration captured
// during StartOAuth and consumed by CompleteOAuth. The client secret never
// lands here — ClientSecretRef names the vault entry holding it.
type oauthFlowConfig struct {
	ClientID        string   `json:"client_id"`
	ClientSecretRef string   `json:"client_secret_ref,omitempty"`
	TokenEndpoint   string   `json:"token_endpoint"`
	AuthStyle       int      `json:"auth_style"`
	Resource        string   `json:"resource,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
	RedirectURI     string   `json:"redirect_uri"`
}

func (c oauthFlowConfig) marshal() (json.RawMessage, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode oauth flow config: %w", err)
	}
	return raw, nil
}

func decodeOAuthFlowConfig(raw json.RawMessage) (oauthFlowConfig, error) {
	var cfg oauthFlowConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return oauthFlowConfig{}, fmt.Errorf("mcp: decode oauth flow config: %w", err)
	}
	if cfg.ClientID == "" || cfg.TokenEndpoint == "" || cfg.RedirectURI == "" {
		return oauthFlowConfig{}, fmt.Errorf("mcp: oauth flow config is incomplete")
	}
	return cfg, nil
}

// loadBundle reads and decodes the token bundle for one registration at the
// owner's vault tuple. A missing entry is not an error: it means "never
// connected", so it yields a nil bundle.
func (s *Service) loadBundle(ctx context.Context, reg Registration, owner CredentialOwner) (*OAuthBundle, error) {
	if s.vault == nil {
		return nil, fmt.Errorf("mcp: oauth requires the vault, which is not configured")
	}
	raw, err := s.vault.GetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, oauthBundleName(reg.ID))
	if err != nil {
		return nil, fmt.Errorf("mcp: read oauth bundle: %w", err)
	}
	if raw == "" {
		return nil, nil
	}
	var bundle OAuthBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("mcp: decode oauth bundle: %w", err)
	}
	return &bundle, nil
}

// storeBundle marshals and writes the bundle at the owner's vault tuple.
func (s *Service) storeBundle(ctx context.Context, reg Registration, owner CredentialOwner, bundle OAuthBundle) error {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("mcp: encode oauth bundle: %w", err)
	}
	if err := s.storeToken(ctx, owner.Scope, owner.UserID, owner.AgentID, oauthBundleName(reg.ID), string(raw)); err != nil {
		return fmt.Errorf("mcp: store oauth bundle: %w", err)
	}
	return nil
}
