package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func (s *Server) ListPlugins(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	plugins, err := access.ListPlugins(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"plugins": flattenRegisteredPlugins(s, plugins)})
}

type pluginView struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	Enabled      bool           `json:"enabled"`
	Config       map[string]any `json:"config"`
	DisplayName  string         `json:"display_name"`
	Description  string         `json:"description"`
	Managed      bool           `json:"managed"`
	AdminVisible bool           `json:"admin_visible"`
	HasConfig    bool           `json:"has_config"`
	HasStatus    bool           `json:"has_status"`
	Capabilities []string       `json:"capabilities"`
	Persisted    bool           `json:"persisted"`
	PersistedID  string         `json:"persisted_id"`
	EnvLocked    bool           `json:"env_locked,omitempty"`
}

func flattenRegisteredPlugins(s *Server, plugins []pkgplugins.RegisteredPlugin) []pluginView {
	envBackend := config.SandboxBackendEnvOverride()
	out := make([]pluginView, 0, len(plugins))
	for _, plugin := range plugins {
		enabled := plugin.State.Enabled
		envLocked := false
		if envBackend != "" && plugin.Kind == config.PluginKindSandbox {
			envLocked = true
			enabled = plugin.Name == envBackend
		}
		out = append(out, pluginView{
			ID:           plugin.Info.ID,
			Kind:         plugin.Kind,
			Name:         plugin.Name,
			Enabled:      enabled,
			Config:       s.pluginHost.RedactConfig(plugin.Info.ID, plugin.State.Config),
			DisplayName:  plugin.Info.DisplayName,
			Description:  plugin.Info.Description,
			Managed:      plugin.Info.Managed,
			AdminVisible: plugin.Info.AdminVisible,
			HasConfig:    plugin.HasConfig,
			HasStatus:    plugin.HasStatus,
			Capabilities: plugin.SortedCapabilities(),
			Persisted:    plugin.Persisted,
			PersistedID:  plugin.PersistedID,
			EnvLocked:    envLocked,
		})
	}
	return out
}

func (s *Server) GetPluginStatus(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	status, err := access.GetPluginStatus(r.Context(), kind, name)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) GetPluginConfig(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	cfg, err := access.GetPluginConfig(r.Context(), kind, name)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, cfg)
}

func (s *Server) GetPluginConfigSchema(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	schema, err := access.GetPluginConfigSchema(r.Context(), kind, name)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, schema)
}

func (s *Server) TogglePlugin(w http.ResponseWriter, r *http.Request, kind string, name string) {
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
	p, err := access.TogglePlugin(r.Context(), kind, name, req.Enabled)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, p)
}

func (s *Server) UpdatePluginConfig(w http.ResponseWriter, r *http.Request, kind string, name string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req struct {
		Config map[string]any `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	p, err := access.UpdatePluginConfig(r.Context(), kind, name, req.Config)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, p)
}
