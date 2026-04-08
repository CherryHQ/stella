package admin

import (
	"net/http"

	internalchannel "github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := s.pluginHost.ListAdminVisiblePlugins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, flattenRegisteredPlugins(s, plugins))
}

type pluginView struct {
	ID                    string         `json:"id"`
	Kind                  string         `json:"kind"`
	Name                  string         `json:"name"`
	Enabled               bool           `json:"enabled"`
	Config                map[string]any `json:"config"`
	DisplayName           string         `json:"display_name"`
	Description           string         `json:"description"`
	Managed               bool           `json:"managed"`
	AdminVisible          bool           `json:"admin_visible"`
	HasConfig             bool           `json:"has_config"`
	HasStatus             bool           `json:"has_status"`
	Capabilities          []string       `json:"capabilities"`
	SupportsNotifications bool           `json:"supports_notifications"`
	Persisted             bool           `json:"persisted"`
	PersistedID           string         `json:"persisted_id"`
}

func flattenRegisteredPlugins(s *Server, plugins []pkgplugins.RegisteredPlugin) []pluginView {
	out := make([]pluginView, 0, len(plugins))
	for _, plugin := range plugins {
		out = append(out, pluginView{
			ID:                    plugin.Meta.ID,
			Kind:                  plugin.Meta.Kind,
			Name:                  plugin.Meta.Name,
			Enabled:               plugin.State.Enabled,
			Config:                s.pluginHost.RedactConfig(plugin.Meta.ID, plugin.State.Config),
			DisplayName:           plugin.Meta.DisplayName,
			Description:           plugin.Meta.Description,
			Managed:               plugin.Meta.Managed,
			AdminVisible:          plugin.Meta.AdminVisible,
			HasConfig:             plugin.Meta.HasConfig,
			HasStatus:             plugin.Meta.HasStatus,
			Capabilities:          plugin.Meta.SortedCapabilities(),
			SupportsNotifications: plugin.Meta.SupportsNotifications,
			Persisted:             plugin.Persisted,
			PersistedID:           plugin.PersistedID,
		})
	}
	return out
}

func (s *Server) getPluginStatus(w http.ResponseWriter, r *http.Request) {
	id := pluginRouteID(r.PathValue("kind"), r.PathValue("name"))
	status, err := s.pluginHost.Status(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) getPluginConfigSchema(w http.ResponseWriter, r *http.Request) {
	id := pluginRouteID(r.PathValue("kind"), r.PathValue("name"))
	writeData(w, http.StatusOK, s.pluginHost.ConfigSchema(id))
}

func (s *Server) togglePlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.pluginHost.SetEnabled(r.Context(), id, req.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-fetch the updated plugin to return current state.
	p, err := s.store.GetPlugin(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Non-host-backed channel plugins still use the admin channel lifecycle hooks.
	if p.Kind == config.PluginKindChannel && !internalchannel.IsHostBackedPlugin(id) {
		if req.Enabled {
			s.startChannel(p.Name)
		} else {
			s.stopChannel(p.Name)
		}
	}
	// Hot-reload tool plugins so the change takes effect without restart.
	if p.Kind == config.PluginKindTool && s.poolManager != nil {
		if err := s.poolManager.ReloadPluginTools(r.Context()); err != nil {
			s.log.Error("failed to reload plugin tools", "plugin", id, "error", err)
		}
	}
	if err := s.pluginHost.ApplyPlugin(r.Context(), id); err != nil {
		s.log.Error("failed to apply plugin runtime", "plugin", id, "error", err)
	}
	// Hot-reload hook plugins so the change takes effect without restart.
	if p.Kind == config.PluginKindHook && s.poolManager != nil {
		if err := s.poolManager.ReloadPluginHooks(r.Context()); err != nil {
			s.log.Error("failed to reload plugin hooks", "plugin", id, "error", err)
		}
	}
	// Hot-reload provider plugins so new sessions pick up the change.
	if p.Kind == config.PluginKindProvider && s.poolManager != nil {
		if err := s.poolManager.ReloadPluginProviders(r.Context()); err != nil {
			s.log.Error("failed to reload plugin providers", "plugin", id, "error", err)
		}
	}
	writeData(w, http.StatusOK, p)
}

func (s *Server) updatePluginConfig(w http.ResponseWriter, r *http.Request) {
	id := pluginRouteID(r.PathValue("kind"), r.PathValue("name"))
	var req struct {
		Config map[string]any `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if err := s.pluginHost.ValidateConfig(id, req.Config); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.pluginHost.Config().Set(r.Context(), id, req.Config); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, err := s.store.GetPlugin(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.pluginHost.ApplyPlugin(r.Context(), id); err != nil {
		s.log.Error("failed to apply plugin runtime", "plugin", id, "error", err)
	}
	writeData(w, http.StatusOK, p)
}

func pluginRouteID(kind, name string) string {
	if kind == name {
		return name
	}
	return config.PluginID(kind, name)
}
