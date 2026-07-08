package server

import (
	"net/http"
	"sort"

	"github.com/CherryHQ/stella/internal/config"
)

// ListModels returns enabled models from provider config + fetched cache,
// filtered to only include models whose provider instance is enabled.
// No provider API calls — reads only from the DB.
func (s *Server) ListModels(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	seen := make(map[string]bool)
	filtered := make([]config.CachedModel, 0)
	providerByID := make(map[string]config.Provider, len(providers))
	modelEnabled := make(map[string]map[string]bool, len(providers))
	add := func(providerID, modelID string, enabled map[string]bool) {
		if providerID == "" || modelID == "" {
			return
		}
		provider, ok := providerByID[providerID]
		if !ok || !provider.Enabled {
			return
		}
		if value, ok := enabled[modelID]; ok && !value {
			return
		}
		key := providerID + "/" + modelID
		if seen[key] {
			return
		}
		seen[key] = true
		filtered = append(filtered, config.CachedModel{Provider: providerID, ProviderName: provider.Name, Model: modelID})
	}
	for _, provider := range providers {
		providerByID[provider.ID] = provider
		enabled := make(map[string]bool, len(provider.Models))
		for modelID, model := range provider.Models {
			enabled[modelID] = model.Enabled
			add(provider.ID, modelID, enabled)
		}
		modelEnabled[provider.ID] = enabled
	}

	if cached, err := s.store.ListCachedModels(r.Context()); err == nil {
		for _, model := range cached {
			add(model.Provider, model.Model, modelEnabled[model.Provider])
		}
	} else {
		s.log.Warn("failed to load cached models", "error", err)
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Provider != filtered[j].Provider {
			return filtered[i].Provider < filtered[j].Provider
		}
		return filtered[i].Model < filtered[j].Model
	})
	writeData(w, http.StatusOK, map[string]any{"models": filtered})
}
