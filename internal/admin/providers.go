package admin

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/providers"
)

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, providers)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if p.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if p.Type == "" {
		p.Type = p.ID
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if !p.Enabled {
		p.Enabled = true
	}
	if err := s.store.CreateProvider(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadProviders(r.Context())
	writeData(w, http.StatusCreated, p)
}

func (s *Server) getProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeData(w, http.StatusOK, p)
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	existing, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	p.ID = id
	if p.Type == "" {
		p.Type = existing.Type
	}
	if p.Name == "" {
		p.Name = id
	}
	if err := s.store.UpdateProvider(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadProviders(r.Context())
	writeData(w, http.StatusOK, p)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteProvider(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadProviders(r.Context())
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// reloadProviders triggers a provider hot-reload if the pool manager is available.
func (s *Server) reloadProviders(ctx context.Context) {
	if s.poolManager == nil {
		return
	}
	if err := s.poolManager.ReloadPluginProviders(ctx); err != nil {
		s.log.Error("failed to reload providers", "error", err)
	}
}

type providerModelItem struct {
	ID      string               `json:"id"`
	Name    string               `json:"name,omitempty"`
	Source  string               `json:"source"`
	Enabled bool                 `json:"enabled"`
	Config  config.ProviderModel `json:"config,omitzero"`
}

func (s *Server) listProviderModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	provider, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	writeData(w, http.StatusOK, s.mergedProviderModels(provider))
}

func (s *Server) fetchProviderModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// If no credentials in body, try loading from stored provider.
	if body.APIKey == "" {
		p, err := s.store.GetProvider(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusBadRequest, "api_key is required")
			return
		}
		body.APIKey = p.APIKey
		if body.BaseURL == "" {
			body.BaseURL = p.BaseURL
		}
	}

	if body.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required")
		return
	}

	providerCfg, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	providerType := providerCfg.Type
	if providerType == "" {
		providerType = providerCfg.ID
	}
	provider, err := s.pluginHost.BuildProvider(providerType, map[string]any{
		"api_key":  body.APIKey,
		"base_url": body.BaseURL,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown provider type: "+providerType)
		return
	}

	lister, ok := provider.(providers.ModelLister)
	if !ok {
		writeError(w, http.StatusBadRequest, id+" does not support model listing")
		return
	}

	fetchCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	listed, err := lister.ListModels(fetchCtx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch failed: "+err.Error())
		return
	}

	modelIDs := make([]string, 0, len(listed))
	for _, m := range listed {
		modelIDs = append(modelIDs, m.ID)
	}

	// Update models cache: replace fetched entries for this provider, but keep
	// user-added provider config models separate so fetches never overwrite them.
	s.updateModelsCache(id, modelIDs)

	providerCfg, err = s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reload provider config: "+err.Error())
		return
	}

	writeData(w, http.StatusOK, s.mergedProviderModels(providerCfg))
}

func (s *Server) mergedProviderModels(provider config.Provider) []providerModelItem {
	items := make(map[string]providerModelItem)
	for id, cfg := range provider.Models {
		name := cfg.Name
		if name == "" {
			name = id
		}
		items[id] = providerModelItem{
			ID:      id,
			Name:    name,
			Source:  "custom",
			Enabled: cfg.Enabled,
			Config:  cfg,
		}
	}

	cache, err := config.LoadModelsCache()
	if err == nil {
		for _, model := range cache.Models {
			if model.Provider != provider.ID {
				continue
			}
			if _, exists := items[model.Model]; exists {
				continue
			}
			items[model.Model] = providerModelItem{
				ID:      model.Model,
				Name:    model.Model,
				Source:  "fetched",
				Enabled: true,
			}
		}
	}

	out := make([]providerModelItem, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// updateModelsCache merges fetched models for a provider into the cache file.
func (s *Server) updateModelsCache(providerID string, modelIDs []string) {
	cache, err := config.LoadModelsCache()
	if err != nil {
		cache = &config.ModelsCache{}
	}

	// Remove old entries for this provider, then add new ones.
	filtered := cache.Models[:0]
	for _, m := range cache.Models {
		if m.Provider != providerID {
			filtered = append(filtered, m)
		}
	}
	for _, id := range modelIDs {
		filtered = append(filtered, config.CachedModel{Provider: providerID, Model: id})
	}

	cache.Models = filtered
	cache.UpdatedAt = time.Now().UTC()

	if err := config.SaveModelsCache(cache); err != nil {
		s.log.Warn("failed to update models cache", "error", err)
	}
}

func (s *Server) listProviderTypes(w http.ResponseWriter, r *http.Request) {
	type providerType struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		DefaultURL string `json:"default_url"`
	}

	types := make([]providerType, 0)
	for _, provider := range s.pluginHost.ListProviderTypes() {
		types = append(types, providerType{
			ID:         provider.ID,
			Name:       provider.Name,
			DefaultURL: provider.DefaultURL,
		})
	}
	writeData(w, http.StatusOK, types)
}
