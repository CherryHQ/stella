package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

type manifestPluginsResponse struct {
	Plugins        []manifest.ManifestPlugin        `json:"plugins"`
	OAuthProviders []manifest.ManifestOAuthProvider `json:"oauth_providers"`
}

func manifestPluginsResponseFrom(m *manifest.Manifest) manifestPluginsResponse {
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

// manifestPluginID rebuilds the plugin ID a two-segment route addresses.
//
// The second segment is the ID's own suffix, not the plugin's name: `name` is an
// ordinary definition field and is allowed to differ.
func manifestPluginID(kind, idSuffix string) string { return kind + "/" + idSuffix }

// SaveManifestPluginDefinition writes one plugin's definition. `fields` names
// what this request takes ownership of on a builtin; an admin-added plugin has
// no definition underneath it, so the body is the whole plugin.
func (s *Server) SaveManifestPluginDefinition(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req struct {
		Plugin manifest.ManifestPlugin `json:"plugin"`
		Fields []string                `json:"fields"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// The URL addresses the plugin, so the ID comes from it. The body's `name` is
	// a definition field and is left alone; a body that claims a different kind is
	// addressing something other than what it asked for.
	req.Plugin.ID = manifestPluginID(kind, name)
	if req.Plugin.Kind != "" && req.Plugin.Kind != kind {
		writeError(w, http.StatusBadRequest, "plugin kind does not match the URL")
		return
	}
	req.Plugin.Kind = kind
	merged, err := access.SaveManifestPluginDefinition(r.Context(), req.Plugin, req.Fields)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, manifestPluginsResponseFrom(merged))
}

// SetManifestPluginEnabled toggles one plugin without touching its definition.
func (s *Server) SetManifestPluginEnabled(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	merged, err := access.SetManifestPluginEnabled(r.Context(), manifestPluginID(kind, name), req.Enabled)
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
	if err := access.DeleteManifestPlugin(r.Context(), manifestPluginID(kind, name)); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResetManifestPlugin hands a builtin's definition back to the running server,
// either one field of it or, with no field named, all of it.
func (s *Server) ResetManifestPlugin(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req struct {
		Field string `json:"field"`
	}
	// An empty body means "reset the whole definition", so only malformed JSON is
	// an error.
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	merged, err := access.ResetManifestPlugin(r.Context(), manifestPluginID(kind, name), req.Field)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, manifestPluginsResponseFrom(merged))
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
