package admin

import (
	"net/http"

	internalchannel "github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	annamcp "github.com/vaayne/anna/internal/mcp"
)

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := s.store.ListPlugins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, plugins)
}

func (s *Server) getPluginStatus(w http.ResponseWriter, r *http.Request) {
	id := pluginRouteID(r.PathValue("kind"), r.PathValue("name"))
	if s.pluginHost == nil {
		writeData(w, http.StatusOK, map[string]any{})
		return
	}
	status, err := s.pluginHost.Status(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, status)
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
	canonicalID := id
	if s.pluginHost != nil {
		canonicalID = s.pluginHost.ResolvePluginID(id)
		if err := s.pluginHost.SetEnabled(r.Context(), canonicalID, req.Enabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else if err := s.store.SetPluginEnabled(r.Context(), id, req.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-fetch the updated plugin to return current state.
	p, err := s.store.GetPlugin(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Hot-reload channel plugins: start on enable, stop on disable.
	if p.Kind == config.PluginKindChannel && canonicalID != internalchannel.TelegramPluginID {
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
	if s.pluginHost != nil {
		if err := s.pluginHost.ApplyPlugin(r.Context(), canonicalID); err != nil {
			s.log.Error("failed to apply plugin runtime", "plugin", canonicalID, "error", err)
		}
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
	canonicalID := id
	if s.pluginHost != nil {
		canonicalID = s.pluginHost.ResolvePluginID(id)
	}
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
	if s.pluginHost != nil {
		if err := s.pluginHost.ValidateConfig(canonicalID, req.Config); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.pluginHost.Config().Set(r.Context(), canonicalID, req.Config); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if id == config.PluginID(config.PluginKindTool, "mcp") {
			if _, err := annamcp.DecodeConfig(req.Config); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if err := s.store.SetPluginConfig(r.Context(), id, req.Config); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	p, err := s.store.GetPlugin(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.pluginHost != nil {
		if err := s.pluginHost.ApplyPlugin(r.Context(), canonicalID); err != nil {
			s.log.Error("failed to apply plugin runtime", "plugin", canonicalID, "error", err)
		}
	}
	writeData(w, http.StatusOK, p)
}

func pluginRouteID(kind, name string) string {
	if kind == name {
		return name
	}
	return config.PluginID(kind, name)
}
