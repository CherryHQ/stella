package server

import (
	"context"
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const channelPluginConfigError = "channel instance config lives on /channels, not plugin config"

func (s *Server) ListPlugins(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	plugins, err := s.pluginHost.ListAdminVisiblePlugins(r.Context())
	if err != nil {
		s.writeInternalError(w, err)
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
	if requireAdmin(w, r) == nil {
		return
	}
	id := pluginRouteID(kind, name)
	status, err := s.pluginHost.Status(r.Context(), id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) GetPluginConfig(w http.ResponseWriter, r *http.Request, kind string, name string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if kind == config.PluginKindChannel {
		writeError(w, http.StatusBadRequest, channelPluginConfigError)
		return
	}
	id := pluginRouteID(kind, name)
	state, err := s.pluginHost.Config().Get(r.Context(), id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, state.Config)
}

func (s *Server) GetPluginConfigSchema(w http.ResponseWriter, r *http.Request, kind string, name string) {
	if requireAdmin(w, r) == nil {
		return
	}
	id := pluginRouteID(kind, name)
	writeData(w, http.StatusOK, s.pluginHost.ConfigSchema(id))
}

func (s *Server) TogglePlugin(w http.ResponseWriter, r *http.Request, kind string, name string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if kind == config.PluginKindSandbox && config.SandboxBackendEnvOverride() != "" {
		writeError(w, http.StatusForbidden, "sandbox backend is locked by STELLA_SANDBOX_BACKEND environment variable")
		return
	}
	id := pluginRouteID(kind, name)
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.pluginHost.SetEnabled(r.Context(), id, req.Enabled); err != nil {
		s.writeInternalError(w, err)
		return
	}
	p, err := s.store.GetPlugin(r.Context(), id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.applyAndReloadPlugin(r.Context(), p)
	writeData(w, http.StatusOK, p)
}

func (s *Server) UpdatePluginConfig(w http.ResponseWriter, r *http.Request, kind string, name string) {
	if requireAdmin(w, r) == nil {
		return
	}
	if kind == config.PluginKindChannel {
		writeError(w, http.StatusBadRequest, channelPluginConfigError)
		return
	}
	id := pluginRouteID(kind, name)
	var req struct {
		Config map[string]any `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if !s.pluginHost.IsConfigurable(id) {
		writeError(w, http.StatusNotFound, "plugin not registered: "+id)
		return
	}
	if err := s.pluginHost.ValidateConfig(id, req.Config); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := s.pluginHost.Config().Set(r.Context(), id, req.Config); err != nil {
		s.writeInternalError(w, err)
		return
	}
	p, err := s.store.GetPlugin(r.Context(), id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.applyAndReloadPlugin(r.Context(), p)
	writeData(w, http.StatusOK, p)
}

// applyAndReloadPlugin updates a plugin's runtime state and hot-reloads
// the pool manager so changes take effect without restart.
func (s *Server) applyAndReloadPlugin(ctx context.Context, p config.Plugin) {
	if err := s.pluginHost.ApplyPlugin(ctx, p.ID); err != nil {
		s.log.Error("failed to apply plugin runtime", "plugin", p.ID, "error", err)
	}
	if s.poolManager == nil {
		return
	}
	switch p.Kind {
	case config.PluginKindTool:
		if err := s.poolManager.ReloadPluginTools(ctx); err != nil {
			s.log.Error("failed to reload plugin tools", "plugin", p.ID, "error", err)
		}
	case config.PluginKindHook:
		if err := s.poolManager.ReloadPluginHooks(ctx); err != nil {
			s.log.Error("failed to reload plugin hooks", "plugin", p.ID, "error", err)
		}
	case config.PluginKindProvider:
		if err := s.poolManager.ReloadPluginProviders(ctx); err != nil {
			s.log.Error("failed to reload plugin providers", "plugin", p.ID, "error", err)
		}
	}
}

func pluginRouteID(kind, name string) string {
	if kind == name {
		return name
	}
	return config.PluginID(kind, name)
}
