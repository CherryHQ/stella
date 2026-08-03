package controlplane

import (
	"context"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/manifestplugins"
)

// EmbeddingUpdate carries a validated embedding-settings write. APIKey is nil to
// keep the stored key (the GET never echoes it) or non-nil to replace it.
type EmbeddingUpdate struct {
	Enabled   bool
	Model     string
	Dim       int
	BaseURL   string
	Normalize bool
	APIKey    *string
}

// GetEmbeddingSettings returns the deployment-wide embedding configuration.
func (a *Access) GetEmbeddingSettings(ctx context.Context) (config.EmbeddingSettings, error) {
	return config.LoadEmbeddingSettings(ctx, a.svc.store)
}

// SetEmbeddingSettings persists the embedding configuration. The api_key is
// write-only: an omitted or empty key keeps the stored one. Enabling the lane
// without a key is rejected (it would silently no-op).
func (a *Access) SetEmbeddingSettings(ctx context.Context, upd EmbeddingUpdate) (config.EmbeddingSettings, error) {
	existing, err := config.LoadEmbeddingSettings(ctx, a.svc.store)
	if err != nil {
		return config.EmbeddingSettings{}, err
	}
	next := config.EmbeddingSettings{
		Enabled:   upd.Enabled,
		Model:     upd.Model,
		Dim:       upd.Dim,
		BaseURL:   upd.BaseURL,
		Normalize: upd.Normalize,
		APIKey:    existing.APIKey, // preserve unless a new key is supplied
	}
	if upd.APIKey != nil && *upd.APIKey != "" {
		next.APIKey = *upd.APIKey
	}
	if next.Enabled && next.APIKey == "" {
		return config.EmbeddingSettings{}, invalid("api_key is required to enable embedding")
	}
	if err := config.SaveEmbeddingSettings(ctx, a.svc.store, next); err != nil {
		return config.EmbeddingSettings{}, err
	}
	return next, nil
}

// GetVisionSettings returns the deployment-wide image-understanding configuration.
func (a *Access) GetVisionSettings(ctx context.Context) (config.VisionSettings, error) {
	return config.LoadVisionSettings(ctx, a.svc.store)
}

// SetVisionSettings persists the vision model. An empty model clears it, which
// returns image understanding to local text extraction. A non-empty model must
// carry its provider prefix: unlike an agent's own tiers there is no agent
// context to infer a default provider from, and a bare model name would resolve
// differently per agent.
func (a *Access) SetVisionSettings(ctx context.Context, s config.VisionSettings) (config.VisionSettings, error) {
	s.Model = strings.TrimSpace(s.Model)
	if s.Model != "" {
		if provider, model := config.ParseModelRef(s.Model); provider == "" || model == "" {
			return config.VisionSettings{}, invalid(`model must be in "provider/model" form`)
		}
	}
	if err := config.SaveVisionSettings(ctx, a.svc.store, s); err != nil {
		return config.VisionSettings{}, err
	}
	return s, nil
}

// SearchCliToolRegistry searches the mise tool registry so the UI can add a CLI
// tool by name instead of a hand-written backend key.
func (a *Access) SearchCliToolRegistry(ctx context.Context, query string, limit int) ([]manifestplugins.RegistryTool, error) {
	return manifestplugins.SearchRegistry(ctx, config.StellaHome(), query, limit)
}

// CliToolLatest resolves the latest installable version for a mise tool key.
func (a *Access) CliToolLatest(ctx context.Context, tool string) (string, error) {
	return manifestplugins.LatestVersion(ctx, config.StellaHome(), tool)
}

// GetOAuthProviderConfig returns the stored OAuth provider client configuration.
func (a *Access) GetOAuthProviderConfig(ctx context.Context, id string) (connections.OAuthProviderConfig, error) {
	return a.svc.conns.GetOAuthProviderConfig(ctx, id)
}

// SetOAuthProviderConfig persists an OAuth provider client configuration and
// returns the refreshed stored value.
func (a *Access) SetOAuthProviderConfig(ctx context.Context, cfg connections.OAuthProviderConfig) (connections.OAuthProviderConfig, error) {
	if err := a.svc.conns.SetOAuthProviderConfig(ctx, cfg); err != nil {
		return connections.OAuthProviderConfig{}, err
	}
	return a.svc.conns.GetOAuthProviderConfig(ctx, cfg.ProviderID)
}

// DeleteOAuthProviderConfig removes an OAuth provider client configuration.
func (a *Access) DeleteOAuthProviderConfig(ctx context.Context, id string) error {
	return a.svc.conns.DeleteOAuthProviderConfig(ctx, id)
}
