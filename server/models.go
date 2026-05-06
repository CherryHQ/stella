package server

import (
	"net/http"
	"sort"

	"github.com/vaayne/anna/internal/config"
)

// listCachedModels returns enabled models from provider config + fetched cache,
// filtered to only include models whose provider instance is enabled.
// No provider API calls — reads only from the DB and ~/.anna/cache/models.json.
func (s *Server) listCachedModels(w http.ResponseWriter, r *http.Request) {
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list provider config: "+err.Error())
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
		filtered = append(filtered, config.CachedModel{Provider: providerID, Model: modelID})
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

	if cache, err := config.LoadModelsCache(); err == nil {
		for _, model := range cache.Models {
			add(model.Provider, model.Model, modelEnabled[model.Provider])
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Provider != filtered[j].Provider {
			return filtered[i].Provider < filtered[j].Provider
		}
		return filtered[i].Model < filtered[j].Model
	})
	writeData(w, http.StatusOK, filtered)
}
