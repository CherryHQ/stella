package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
)

func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	pList, err := access.ListProviders(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"providers": pList})
}

func (s *Server) CreateProvider(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
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
	if err := access.CreateProvider(r.Context(), p); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusCreated, p)
}

func (s *Server) GetProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	p, err := access.GetProvider(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, p)
}

func (s *Server) UpdateProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	saved, err := access.UpdateProvider(r.Context(), id, p)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, saved)
}

func (s *Server) DeleteProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.DeleteProvider(r.Context(), id); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ListProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	models, err := access.ListProviderModels(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) FetchProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
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
	models, err := access.FetchProviderModels(r.Context(), id, body.APIKey, body.BaseURL)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) ListProviderTypes(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	types, err := access.ListProviderTypes()
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}

	type providerType struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		DefaultURL string `json:"default_url"`
	}
	out := make([]providerType, 0, len(types))
	for _, pt := range types {
		out = append(out, providerType{ID: pt.ID, Name: pt.Name, DefaultURL: pt.DefaultURL})
	}
	writeData(w, http.StatusOK, map[string]any{"provider_types": out})
}
