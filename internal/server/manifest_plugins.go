package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

type manifestPluginsResponse struct {
	Plugins        []manifestplugins.ManifestPlugin        `json:"plugins"`
	OAuthProviders []manifestplugins.ManifestOAuthProvider `json:"oauth_providers"`
}

func manifestPluginsResponseFrom(m *manifestplugins.Manifest) manifestPluginsResponse {
	return manifestPluginsResponse{Plugins: m.Plugins, OAuthProviders: m.OAuthProviders}
}

func (s *Server) ListManifestPlugins(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	merged, err := access.ListManifestPlugins(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, manifestPluginsResponseFrom(merged))
}

func (s *Server) SaveManifestPlugins(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req struct {
		Plugins []manifestplugins.ManifestPlugin `json:"plugins"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	merged, err := access.SaveManifestPlugins(r.Context(), req.Plugins)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, manifestPluginsResponseFrom(merged))
}

// DeleteManifestPlugin removes an admin-added plugin. The id is addressed as
// kind/name, the same shape the builtin plugin routes use.
func (s *Server) DeleteManifestPlugin(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.DeleteManifestPlugin(r.Context(), kind+"/"+name); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SyncManifestPlugins(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	result, err := access.SyncManifestPlugins(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, result)
}
