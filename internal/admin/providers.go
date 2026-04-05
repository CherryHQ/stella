package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/providers"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
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
	if p.Name == "" {
		p.Name = p.ID
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
	p.ID = id
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

	provider := newProviderFromCreds(id, body.APIKey, body.BaseURL)
	if provider == nil {
		writeError(w, http.StatusBadRequest, "unknown provider: "+id)
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

	// Update models cache: merge newly fetched models with existing cache.
	s.updateModelsCache(id, modelIDs)

	writeData(w, http.StatusOK, modelIDs)
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

	metas := pluginproviders.Metas()
	types := make([]providerType, 0, len(metas))
	for _, name := range pluginproviders.Names() {
		m := metas[name]
		types = append(types, providerType{
			ID:         name,
			Name:       m.Name,
			DefaultURL: m.DefaultURL,
		})
	}
	writeData(w, http.StatusOK, types)
}

// newProviderFromCreds creates a providers.ProviderAdapter from raw credentials.
func newProviderFromCreds(name, apiKey, baseURL string) providers.ProviderAdapter {
	p, err := pluginproviders.Build(name, pluginproviders.ProviderConfig{APIKey: apiKey, BaseURL: baseURL})
	if err != nil {
		return nil
	}
	return p
}
