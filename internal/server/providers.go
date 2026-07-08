package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/providers"
)

func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	pList, err := s.store.ListProviders(r.Context())
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"providers": pList})
}

func (s *Server) CreateProvider(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
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
	ctx := r.Context()
	if err := s.store.CreateProvider(ctx, p); err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.reloadProviders(ctx)
	writeData(w, http.StatusCreated, p)
}

func (s *Server) GetProvider(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	p, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeData(w, http.StatusOK, p)
}

func (s *Server) UpdateProvider(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	existing, err := s.store.GetProvider(ctx, id)
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
	if err := s.store.UpdateProvider(ctx, p); err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.reloadProviders(ctx)
	writeData(w, http.StatusOK, p)
}

func (s *Server) DeleteProvider(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetProvider(ctx, id); err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if err := s.store.DeleteProvider(ctx, id); err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.reloadProviders(ctx)
	writeNoContent(w)
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

func (s *Server) ListProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	provider, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	writeData(w, http.StatusOK, map[string]any{"models": s.mergedProviderModels(r.Context(), provider)})
}

func (s *Server) FetchProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}

	var body struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
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
		s.writeBadGatewayError(w, err)
		return
	}

	modelIDs := make([]string, 0, len(listed))
	for _, m := range listed {
		modelIDs = append(modelIDs, m.ID)
	}

	// Update models cache: replace fetched entries for this provider, but keep
	// user-added provider config models separate so fetches never overwrite them.
	if err := s.store.ReplaceCachedModels(r.Context(), id, modelIDs); err != nil {
		s.log.Warn("failed to update models cache", "provider", id, "error", err)
	}

	providerCfg, err = s.store.GetProvider(r.Context(), id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeData(w, http.StatusOK, map[string]any{"models": s.mergedProviderModels(r.Context(), providerCfg)})
}

func (s *Server) mergedProviderModels(ctx context.Context, provider config.Provider) []providerModelItem {
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

	cached, err := s.store.ListCachedModels(ctx)
	if err != nil {
		s.log.Warn("failed to load cached models", "provider", provider.ID, "error", err)
	}
	for _, model := range cached {
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

func (s *Server) ListProviderTypes(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}

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
	writeData(w, http.StatusOK, map[string]any{"provider_types": types})
}
