package admin

import (
	"encoding/json"
	"net/http"

	"github.com/vaayne/anna/internal/config"
)

// channelView is the JSON shape the admin frontend expects for channel objects.
// It matches the legacy settings_channels format: config is a JSON string.
type channelView struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Config  string `json:"config"`
}

func pluginToChannelView(p config.Plugin) channelView {
	cfgJSON, _ := json.Marshal(p.Config)
	return channelView{
		ID:      p.Name,
		Enabled: p.Enabled,
		Config:  string(cfgJSON),
	}
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	plugins, err := s.store.ListPluginsByKind(r.Context(), config.PluginKindChannel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]channelView, len(plugins))
	for i, p := range plugins {
		views[i] = pluginToChannelView(p)
	}
	writeData(w, http.StatusOK, views)
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	pluginID := config.PluginID(config.PluginKindChannel, platform)
	p, err := s.store.GetPlugin(r.Context(), pluginID)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeData(w, http.StatusOK, pluginToChannelView(p))
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	var req struct {
		Enabled bool   `json:"enabled"`
		Config  string `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	var cfgMap map[string]any
	if req.Config != "" {
		if err := json.Unmarshal([]byte(req.Config), &cfgMap); err != nil {
			writeError(w, http.StatusBadRequest, "invalid config JSON: "+err.Error())
			return
		}
	}
	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}

	pluginID := config.PluginID(config.PluginKindChannel, platform)
	if err := s.pluginHost.ValidateConfig(pluginID, cfgMap); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.pluginHost.Config().Set(r.Context(), pluginID, cfgMap); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.pluginHost.SetEnabled(r.Context(), pluginID, req.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.pluginHost.ApplyPlugin(r.Context(), pluginID); err != nil {
		s.log.Error("failed to apply plugin runtime", "plugin", pluginID, "error", err)
	}
	p, err := s.store.GetPlugin(r.Context(), pluginID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, pluginToChannelView(p))
}
