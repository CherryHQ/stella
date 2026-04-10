package admin

import (
	"net/http"
	"sort"

	"github.com/vaayne/anna/internal/config"
)

// listCachedModels returns enabled models from provider config + fetched cache,
// filtered to only include models whose provider plugin is currently enabled.
// No provider API calls — reads only from the DB and ~/.anna/cache/models.json.
func (s *Server) listCachedModels(w http.ResponseWriter, r *http.Request) {
	plugins, err := s.store.ListPluginsByKind(r.Context(), config.PluginKindProvider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list providers: "+err.Error())
		return
	}
	providerEnabled := make(map[string]bool, len(plugins))
	for _, p := range plugins {
		if p.Enabled {
			providerEnabled[p.Name] = true
		}
	}

	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list provider config: "+err.Error())
		return
	}

	seen := make(map[string]bool)
	filtered := make([]config.CachedModel, 0)
	add := func(providerID, modelID string, disabled map[string]bool) {
		if providerID == "" || modelID == "" || !providerEnabled[providerID] || disabled[modelID] {
			return
		}
		key := providerID + "/" + modelID
		if seen[key] {
			return
		}
		seen[key] = true
		filtered = append(filtered, config.CachedModel{Provider: providerID, Model: modelID})
	}

	providerDisabled := make(map[string]map[string]bool, len(providers))
	for _, provider := range providers {
		disabled := disabledModelSet(provider.DisabledModels)
		providerDisabled[provider.ID] = disabled
		for modelID := range provider.Models {
			add(provider.ID, modelID, disabled)
		}
	}

	if cache, err := config.LoadModelsCache(); err == nil {
		for _, model := range cache.Models {
			add(model.Provider, model.Model, providerDisabled[model.Provider])
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
