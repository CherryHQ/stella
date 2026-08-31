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

// EmbeddingState is the lane as an admin needs to see it: what is stored, plus
// whether it is actually running. The two differ whenever the embedding model
// does not resolve to a provider with a key, and only the server can tell —
// which is why Active travels with the settings instead of being re-derived in
// the browser from a partial view of the catalog.
type EmbeddingState struct {
	Settings config.EmbeddingSettings
	Active   bool
}

// GetEmbeddingSettings returns the deployment-wide embedding configuration.
func (a *Access) GetEmbeddingSettings(ctx context.Context) (EmbeddingState, error) {
	s, err := config.LoadEmbeddingSettings(ctx, a.svc.store)
	if err != nil {
		return EmbeddingState{}, err
	}
	return a.embeddingState(ctx, s)
}

func (a *Access) embeddingState(ctx context.Context, s config.EmbeddingSettings) (EmbeddingState, error) {
	rt, err := config.ResolveEmbedding(ctx, a.svc.store)
	if err != nil {
		return EmbeddingState{}, err
	}
	return EmbeddingState{Settings: s, Active: rt.Enabled}, nil
}

// SetEmbeddingSettings persists the embedding lane's knobs.
//
// Enabling the lane before its model resolves is allowed on purpose. The model
// lives in DefaultModels, so the two are separate writes, and rejecting here
// would make the admin's success depend on which one they saved first — with a
// half-applied deployment whenever the second call failed. config.ResolveEmbedding
// treats an unresolvable reference as disabled instead, so the stored flag is an
// intent that turns itself on the moment the model behind it resolves.
func (a *Access) SetEmbeddingSettings(ctx context.Context, upd EmbeddingUpdate) (EmbeddingState, error) {
	if err := validateEmbeddingDim(upd.Dim); err != nil {
		return EmbeddingState{}, err
	}
	next := config.EmbeddingSettings{
		Enabled:   upd.Enabled,
		Dim:       upd.Dim,
		Normalize: upd.Normalize,
	}
	if err := config.SaveEmbeddingSettings(ctx, a.svc.store, next); err != nil {
		return EmbeddingState{}, err
	}
	return a.embeddingState(ctx, next)
}

func (a *Access) conditionalSettings() (config.ConditionalSettingStore, error) {
	store, ok := a.svc.store.(config.ConditionalSettingStore)
	if !ok {
		return nil, fmt.Errorf("conditional settings store is unavailable")
	}
	return store, nil
}

// SetEmbeddingSettingsIfVersion closes the read/write race for a Settings tool
// while leaving the existing HTTP write contract untouched.
func (a *Access) SetEmbeddingSettingsIfVersion(ctx context.Context, upd EmbeddingUpdate, expectedVersion string) (EmbeddingState, error) {
	if err := validateEmbeddingDim(upd.Dim); err != nil {
		return EmbeddingState{}, err
	}
	store, err := a.conditionalSettings()
	if err != nil {
		return EmbeddingState{}, err
	}
	raw, err := store.GetSetting(ctx, config.EmbeddingSettingKey)
	if err != nil {
		return EmbeddingState{}, err
	}
	current, err := config.LoadEmbeddingSettings(ctx, store)
	if err != nil {
		return EmbeddingState{}, err
	}
	if deploymentVersion(current) != expectedVersion {
		return EmbeddingState{}, &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	next := config.EmbeddingSettings{Enabled: upd.Enabled, Dim: upd.Dim, Normalize: upd.Normalize}
	updated, err := config.SaveEmbeddingSettingsIfValue(ctx, store, raw, next)
	if err != nil {
		return EmbeddingState{}, err
	}
	if !updated {
		return EmbeddingState{}, &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	return a.embeddingState(ctx, next)
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
	if err := validateDefaultModels(d); err != nil {
		return config.DefaultModels{}, err
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

// SetDefaultModelsIfVersion is the atomic Settings-tool write path. The raw
// app_setting value is compared in SQL, so a concurrent UI write cannot be
// overwritten after the tool's read.
func (a *Access) SetDefaultModelsIfVersion(ctx context.Context, d config.DefaultModels, expectedVersion string) (config.DefaultModels, error) {
	if err := validateDefaultModels(d); err != nil {
		return config.DefaultModels{}, err
	}
	store, err := a.conditionalSettings()
	if err != nil {
		return config.DefaultModels{}, err
	}
	raw, err := store.GetSetting(ctx, config.DefaultModelsSettingKey)
	if err != nil {
		return config.DefaultModels{}, err
	}
	current, err := config.LoadDefaultModels(ctx, store)
	if err != nil {
		return config.DefaultModels{}, err
	}
	if deploymentVersion(current) != expectedVersion {
		return config.DefaultModels{}, &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	updated, err := config.SaveDefaultModelsIfValue(ctx, store, raw, d)
	if err != nil {
		return config.DefaultModels{}, err
	}
	if !updated {
		return config.DefaultModels{}, &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	if a.svc.pools != nil {
		if err := a.svc.pools.ReloadModelDefaults(ctx); err != nil {
			a.svc.log.Error("failed to reload default models", "error", err)
		}
	}
	return a.GetDefaultModels(ctx)
}

func validateDefaultModels(d config.DefaultModels) error {
	field, value, isModel, ok := config.ValidateDefaultModels(d)
	if ok {
		return nil
	}
	if isModel {
		return invalid(fmt.Sprintf("invalid %s %q: expected \"provider/model\"", field, value))
	}
	return invalid(fmt.Sprintf("invalid %s %q", field, value))
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
