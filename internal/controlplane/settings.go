package controlplane

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/manifestplugins"
)

// EmbeddingUpdate carries a validated embedding-settings write. It covers only
// the lane's own knobs: the embedding model and its credentials are named in
// DefaultModels alongside every other model role.
type EmbeddingUpdate struct {
	Enabled   bool
	Dim       int
	Normalize bool
}

// GetEmbeddingSettings returns the deployment-wide embedding configuration.
func (a *Access) GetEmbeddingSettings(ctx context.Context) (config.EmbeddingSettings, error) {
	return config.LoadEmbeddingSettings(ctx, a.svc.store)
}

// SetEmbeddingSettings persists the embedding lane's knobs.
//
// Enabling the lane before its model resolves is allowed on purpose. The model
// lives in DefaultModels, so the two are separate writes, and rejecting here
// would make the admin's success depend on which one they saved first — with a
// half-applied deployment whenever the second call failed. config.ResolveEmbedding
// treats an unresolvable reference as disabled instead, so the stored flag is an
// intent that turns itself on the moment the model behind it resolves.
func (a *Access) SetEmbeddingSettings(ctx context.Context, upd EmbeddingUpdate) (config.EmbeddingSettings, error) {
	next := config.EmbeddingSettings{
		Enabled:   upd.Enabled,
		Dim:       upd.Dim,
		Normalize: upd.Normalize,
	}
	if err := config.SaveEmbeddingSettings(ctx, a.svc.store, next); err != nil {
		return config.EmbeddingSettings{}, err
	}
	return next, nil
}

// GetDefaultModels returns the deployment-wide default model configuration.
func (a *Access) GetDefaultModels(ctx context.Context) (config.DefaultModels, error) {
	return config.LoadDefaultModels(ctx, a.svc.store)
}

// SetDefaultModels persists the deployment-wide default models and rebuilds the
// runner factories so newly admitted runners see them. An empty field clears
// that role; a non-empty one must carry its provider prefix, since unlike an
// agent tier there is no agent context to infer a provider from and a bare model
// name would resolve differently per agent.
//
// Only shape is validated, not provider existence: a provider row can be deleted
// after a default references it, so "this provider is configured" is not an
// invariant a write-time check can hold, and a fresh deployment legitimately
// names its models before the provider rows exist.
func (a *Access) SetDefaultModels(ctx context.Context, d config.DefaultModels) (config.DefaultModels, error) {
	for _, f := range []struct{ field, value string }{
		{"model", d.Model},
		{"model_strong", d.ModelStrong},
		{"model_fast", d.ModelFast},
		{"model_vision", d.ModelVision},
		{"model_embedding", d.ModelEmbedding},
	} {
		if !config.ValidModelRef(f.value) {
			return config.DefaultModels{}, invalid(fmt.Sprintf("invalid %s %q: expected \"provider/model\"", f.field, f.value))
		}
	}
	for _, f := range []struct{ field, value string }{
		{"model_thinking", d.ModelThinking},
		{"model_strong_thinking", d.ModelStrongThinking},
		{"model_fast_thinking", d.ModelFastThinking},
	} {
		if !config.ValidThinkingLevel(f.value) {
			return config.DefaultModels{}, invalid(fmt.Sprintf("invalid %s %q", f.field, f.value))
		}
	}
	if err := config.SaveDefaultModels(ctx, a.svc.store, d); err != nil {
		return config.DefaultModels{}, err
	}
	if a.svc.pools != nil {
		if err := a.svc.pools.ReloadModelDefaults(ctx); err != nil {
			a.svc.log.Error("failed to reload default models", "error", err)
		}
	}
	return a.GetDefaultModels(ctx)
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
