package admin

import (
	"net/http"

	"github.com/vaayne/anna/internal/config"
)

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := s.store.ListPlugins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, plugins)
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
	if err := s.store.SetPluginEnabled(r.Context(), id, req.Enabled); err != nil {
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
	if p.Kind == config.PluginKindChannel {
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
	// Hot-reload reflect: start on enable, stop on disable.
	if p.ID == "reflect" {
		if req.Enabled {
			s.startReflect()
		} else {
			s.stopReflect()
		}
	}
	writeData(w, http.StatusOK, p)
}
