package controlplane

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/modelcatalog"
	"github.com/CherryHQ/stella/internal/modelresolve"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/pkg/providers"
)

// ProviderModelItem is one merged model entry for a provider: custom
// (config-declared) models plus fetched (cached) models, unioned. Kept here with
// the persistence it derives from; the transport writes it directly.
type ProviderModelItem struct {
	ID       string                        `json:"id"`
	Name     string                        `json:"name,omitempty"`
	Source   string                        `json:"source"`
	Enabled  bool                          `json:"enabled"`
	Config   config.ProviderModel          `json:"config,omitzero"`
	Override *config.ProviderModelOverride `json:"override,omitempty"`
	Catalog  *modelcatalog.Model           `json:"catalog,omitempty"`
}

// ListProviders returns every configured LLM provider.
func (a *Access) ListProviders(ctx context.Context) ([]config.Provider, error) {
	return a.svc.store.ListProviders(ctx)
}

// CreateProvider persists a new provider and hot-reloads the pool.
func (a *Access) CreateProvider(ctx context.Context, p config.Provider) error {
	if err := a.svc.store.CreateProvider(ctx, p); err != nil {
		return err
	}
	a.svc.reloadProviders(ctx)
	return nil
}

// GetProvider returns one provider by id (opaque 404 when missing).
func (a *Access) GetProvider(ctx context.Context, id string) (config.Provider, error) {
	p, err := a.svc.store.GetProvider(ctx, id)
	if err != nil {
		return config.Provider{}, notFound("provider not found")
	}
	return p, nil
}

type conditionalProviderStore interface {
	GetProviderSnapshot(context.Context, string) (config.ProviderSnapshot, error)
	ListProviderSnapshots(context.Context) ([]config.ProviderSnapshot, error)
	UpdateProviderIfVersion(context.Context, config.Provider, string) (bool, error)
	DeleteProviderIfVersion(context.Context, string, string) (bool, error)
}

func (a *Access) providerCAS() (conditionalProviderStore, error) {
	store, ok := a.svc.store.(conditionalProviderStore)
	if !ok {
		return nil, fmt.Errorf("conditional Provider store is unavailable")
	}
	return store, nil
}

// GetProviderSnapshot returns settings-safe fields and the CAS version from
// one durable read. Keeping them together prevents a stale projection from
// inheriting a newer version and overwriting an intervening admin change.
func (a *Access) GetProviderSnapshot(ctx context.Context, id string) (config.ProviderSnapshot, error) {
	store, err := a.providerCAS()
	if err != nil {
		return config.ProviderSnapshot{}, err
	}
	snapshot, err := store.GetProviderSnapshot(ctx, id)
	if err != nil {
		return config.ProviderSnapshot{}, notFound("provider not found")
	}
	return snapshot, nil
}

func (a *Access) ListProviderSnapshots(ctx context.Context) ([]config.ProviderSnapshot, error) {
	store, err := a.providerCAS()
	if err != nil {
		return nil, err
	}
	return store.ListProviderSnapshots(ctx)
}

// UpdateProviderIfVersion writes only when the database row still has the
// version the Settings tool read. This protects a hidden key from being copied
// over a concurrent credential change.
func (a *Access) UpdateProviderIfVersion(ctx context.Context, p config.Provider, version string) (config.Provider, error) {
	store, err := a.providerCAS()
	if err != nil {
		return config.Provider{}, err
	}
	updated, err := store.UpdateProviderIfVersion(ctx, p, version)
	if err != nil {
		return config.Provider{}, err
	}
	if !updated {
		return config.Provider{}, &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	a.svc.reloadProviders(ctx)
	return p, nil
}

func (a *Access) DeleteProviderIfVersion(ctx context.Context, id, version string) error {
	store, err := a.providerCAS()
	if err != nil {
		return err
	}
	deleted, err := store.DeleteProviderIfVersion(ctx, id, version)
	if err != nil {
		return err
	}
	if !deleted {
		return &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	a.svc.reloadProviders(ctx)
	return nil
}

// UpdateProvider merges the request over the stored provider (preserving Type and
// defaulting Name), persists it, and hot-reloads the pool. It returns the merged
// provider so the transport can echo it.
func (a *Access) UpdateProvider(ctx context.Context, id string, p config.Provider) (config.Provider, error) {
	existing, err := a.svc.store.GetProvider(ctx, id)
	if err != nil {
		return config.Provider{}, notFound("provider not found")
	}
	p.ID = id
	if p.Type == "" {
		p.Type = existing.Type
	}
	if p.Name == "" {
		p.Name = id
	}
	if err := a.svc.store.UpdateProvider(ctx, p); err != nil {
		return config.Provider{}, err
	}
	a.svc.reloadProviders(ctx)
	return p, nil
}

// DeleteProvider removes a provider and hot-reloads the pool.
func (a *Access) DeleteProvider(ctx context.Context, id string) error {
	if _, err := a.svc.store.GetProvider(ctx, id); err != nil {
		return notFound("provider not found")
	}
	if err := a.svc.store.DeleteProvider(ctx, id); err != nil {
		return err
	}
	a.svc.reloadProviders(ctx)
	return nil
}

// ListProviderModels returns the merged custom+fetched model list for a provider.
func (a *Access) ListProviderModels(ctx context.Context, id string) ([]ProviderModelItem, error) {
	provider, err := a.svc.store.GetProvider(ctx, id)
	if err != nil {
		return nil, notFound("provider not found")
	}
	return a.svc.mergedProviderModels(ctx, provider), nil
}

// ResolveProviderModel exposes the same effective merge used by the model list
// and runtime snapshot, preventing evidence and billing from drifting apart.
func (a *Access) ResolveProviderModel(ctx context.Context, id, modelID string) (modelresolve.Result, error) {
	provider, err := a.svc.store.GetProvider(ctx, id)
	if err != nil {
		return modelresolve.Result{}, notFound("provider not found")
	}
	cached, err := a.svc.store.ListCachedModels(ctx)
	if err != nil {
		return modelresolve.Result{}, err
	}
	fetched := false
	for _, model := range cached {
		if model.Provider == id && model.Model == modelID {
			fetched = true
			break
		}
	}
	return modelresolve.Resolve(provider, modelID, fetched, a.svc.effectiveModelCatalog(ctx)), nil
}

// FetchProviderModels lists the provider's models from its live API, refreshes the
// cached set, and returns the merged list. Credentials fall back to the stored
// provider when the caller omits them. It preserves the legacy status split: a
// missing provider during the credential fallback is a 400 "api_key is required",
// while the authoritative load is a 404.
func (a *Access) FetchProviderModels(ctx context.Context, id, apiKey, baseURL string) ([]ProviderModelItem, error) {
	if apiKey == "" {
		p, err := a.svc.store.GetProvider(ctx, id)
		if err != nil {
			return nil, invalid("api_key is required")
		}
		apiKey = p.APIKey
		if baseURL == "" {
			baseURL = p.BaseURL
		}
	}
	if apiKey == "" {
		return nil, invalid("api_key is required")
	}

	providerCfg, err := a.svc.store.GetProvider(ctx, id)
	if err != nil {
		return nil, notFound("provider not found")
	}

	providerType := providerCfg.Type
	if providerType == "" {
		providerType = providerCfg.ID
	}
	provider, err := a.svc.plugins.BuildProvider(providerType, map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
	})
	if err != nil {
		return nil, invalid("unknown provider type: " + providerType)
	}

	lister, ok := provider.(providers.ModelLister)
	if !ok {
		return nil, invalid(id + " does not support model listing")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	listed, err := lister.ListModels(fetchCtx)
	if err != nil {
		return nil, &UpstreamError{Err: err}
	}

	modelIDs := make([]string, 0, len(listed))
	for _, m := range listed {
		modelIDs = append(modelIDs, m.ID)
	}

	// Replace only fetched entries for this provider; user-added config models stay
	// separate so a fetch never overwrites them. A cache-write failure is logged and
	// tolerated, exactly as before.
	if err := a.svc.store.ReplaceCachedModels(ctx, id, modelIDs); err != nil {
		a.svc.log.Warn("failed to update models cache", "provider", id, "error", err)
	}

	providerCfg, err = a.svc.store.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}

	return a.svc.mergedProviderModels(ctx, providerCfg), nil
}

// ListProviderTypes returns the provider plugin types the deployment can add.
func (a *Access) ListProviderTypes() ([]pluginhost.ProviderType, error) {
	return a.svc.plugins.ListProviderTypes(), nil
}

// reloadProviders hot-reloads plugin providers when a pool manager is present.
func (s *Service) reloadProviders(ctx context.Context) {
	if s.pools == nil {
		return
	}
	if err := s.pools.ReloadPluginProviders(ctx); err != nil {
		s.log.Error("failed to reload providers", "error", err)
	}
}

func (s *Service) mergedProviderModels(ctx context.Context, provider config.Provider) []ProviderModelItem {
	catalog := s.effectiveModelCatalog(ctx)
	fetched := map[string]bool{}
	if cached, err := s.store.ListCachedModels(ctx); err == nil {
		for _, model := range cached {
			if model.Provider == provider.ID {
				fetched[model.Model] = true
			}
		}
	} else {
		s.log.Warn("failed to load cached models", "provider", provider.ID, "error", err)
	}
	ids := make(map[string]bool, len(fetched)+len(provider.Models))
	for id := range fetched {
		ids[id] = true
	}
	for id := range provider.Models {
		ids[id] = true
	}
	if p, ok := catalog.Lookup(provider.CatalogID); ok {
		for id := range p.Models {
			ids[id] = true
		}
	}
	out := make([]ProviderModelItem, 0, len(ids))
	for id := range ids {
		resolved := modelresolve.Resolve(provider, id, fetched[id], catalog)
		if !resolved.Found {
			continue
		}
		out = append(out, ProviderModelItem{ID: id, Name: resolved.Model.Name, Source: resolved.Source, Enabled: resolved.Model.Enabled, Config: resolved.Model, Override: resolved.Override, Catalog: resolved.Catalog})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		return out[i].ID < out[j].ID
	})
	return out
}
