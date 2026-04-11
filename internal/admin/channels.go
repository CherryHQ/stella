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
	Type    string `json:"type"`
	AgentID string `json:"agent_id,omitempty"`
	Enabled bool   `json:"enabled"`
	Config  string `json:"config"`
}

func channelToView(ch config.Channel) channelView {
	return channelView{
		ID:      ch.ID,
		Type:    ch.Type,
		AgentID: ch.AgentID,
		Enabled: ch.Enabled,
		Config:  ch.Config,
	}
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]channelView, len(channels))
	for i, ch := range channels {
		views[i] = channelToView(ch)
	}
	writeData(w, http.StatusOK, views)
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeData(w, http.StatusOK, channelToView(ch))
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Type    *string `json:"type"`
		AgentID *string `json:"agent_id"`
		Enabled bool    `json:"enabled"`
		Config  string  `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	existing, existingErr := s.store.GetChannel(r.Context(), id)
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

	channelType := id
	if req.Type != nil && *req.Type != "" {
		channelType = *req.Type
	} else if existingErr == nil && existing.Type != "" {
		channelType = existing.Type
	}
	agentID := ""
	if req.AgentID != nil {
		agentID = *req.AgentID
	} else if existingErr == nil {
		agentID = existing.AgentID
	}
	ch := config.Channel{
		ID:      id,
		Type:    channelType,
		AgentID: agentID,
		Enabled: req.Enabled,
	}
	s.saveChannel(w, r, ch, cfgMap, http.StatusOK)
}

func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request, ch config.Channel, cfgMap map[string]any, status int) bool {
	pluginID := config.PluginID(config.PluginKindChannel, ch.Type)
	if err := s.pluginHost.ValidateConfig(pluginID, cfgMap); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	cfgJSON, err := json.Marshal(cfgMap)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON: "+err.Error())
		return false
	}
	ch.Config = string(cfgJSON)
	if err := s.store.UpsertChannel(r.Context(), ch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if ch.ID == ch.Type {
		if err := s.store.UpsertPlugin(r.Context(), config.Plugin{
			ID:      pluginID,
			Kind:    config.PluginKindChannel,
			Name:    ch.Type,
			Enabled: ch.Enabled,
			Config:  cfgMap,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
	}
	if err := s.pluginHost.ApplyChannel(r.Context(), ch); err != nil {
		s.log.Error("failed to apply channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
	}
	saved, err := s.store.GetChannel(r.Context(), ch.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	writeData(w, status, channelToView(saved))
	return true
}

func parseChannelConfig(raw string) (map[string]any, error) {
	var cfgMap map[string]any
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfgMap); err != nil {
			return nil, err
		}
	}
	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}
	return cfgMap, nil
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		AgentID string `json:"agent_id"`
		Enabled bool   `json:"enabled"`
		Config  string `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ID == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "id and type are required")
		return
	}
	cfgMap, err := parseChannelConfig(req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON: "+err.Error())
		return
	}
	ch := config.Channel{ID: req.ID, Type: req.Type, AgentID: req.AgentID, Enabled: req.Enabled}
	s.saveChannel(w, r, ch, cfgMap, http.StatusCreated)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	ch.Enabled = false
	if err := s.pluginHost.ApplyChannel(r.Context(), ch); err != nil {
		s.log.Error("failed to stop channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
	}
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"deleted": true})
}
