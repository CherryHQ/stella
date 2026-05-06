package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/vaayne/anna/internal/config"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
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

type publicChannelView struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Label     string `json:"label"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type channelWriteRequest struct {
	ID      string  `json:"id"`
	Type    *string `json:"type"`
	AgentID *string `json:"agent_id"`
	Config  string  `json:"config"`
}

var channelLinkLabels = map[string]string{
	pkgchannel.PlatformTelegram: "Telegram",
	pkgchannel.PlatformQQ:       "QQ",
	pkgchannel.PlatformFeishu:   "Feishu",
	pkgchannel.PlatformWeixin:   "Weixin",
}

var channelLinkOrder = []string{
	pkgchannel.PlatformTelegram,
	pkgchannel.PlatformQQ,
	pkgchannel.PlatformFeishu,
	pkgchannel.PlatformWeixin,
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

func (s *Server) listPublicChannels(w http.ResponseWriter, r *http.Request) {
	enabledTypes, err := s.enabledChannelTypes(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	agentNames, err := s.accessibleAgentNames(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, buildPublicChannelViews(channels, enabledTypes, agentNames))
}

func (s *Server) enabledChannelTypes(r *http.Request) (map[string]bool, error) {
	plugins, err := s.store.ListPluginsByKind(r.Context(), config.PluginKindChannel)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(plugins))
	for _, plugin := range plugins {
		if plugin.Enabled {
			enabled[plugin.Name] = true
		}
	}
	return enabled, nil
}

func (s *Server) accessibleAgentNames(r *http.Request) (map[string]string, error) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		return nil, err
	}
	info := UserFromContext(r.Context())
	if info != nil && !info.IsAdmin {
		agents, err = s.filterAccessibleAgents(r.Context(), info, agents)
		if err != nil {
			s.log.Error("filter accessible agents for public channels", "user_id", info.UserID, "error", err)
			return nil, fmt.Errorf("failed to filter agents")
		}
	}
	names := make(map[string]string, len(agents))
	for _, agent := range agents {
		if agent.Enabled {
			names[agent.ID] = agent.Name
		}
	}
	return names, nil
}

func buildPublicChannelViews(channels []config.Channel, enabledTypes map[string]bool, agentNames map[string]string) []publicChannelView {
	views := make([]publicChannelView, 0, len(channels))
	for _, ch := range channels {
		channelType := effectiveChannelType(ch)
		if !ch.Enabled || !enabledTypes[channelType] || ch.ID != channelType {
			continue
		}

		agentName := ""
		if ch.AgentID != "" {
			var ok bool
			agentName, ok = agentNames[ch.AgentID]
			if !ok {
				continue
			}
		}

		views = append(views, publicChannelView{
			ID:        ch.ID,
			Type:      channelType,
			Label:     channelLabel(channelType),
			AgentID:   ch.AgentID,
			AgentName: agentName,
			Enabled:   true,
		})
	}
	sortPublicChannels(views)
	return views
}

func effectiveChannelType(ch config.Channel) string {
	if ch.Type != "" {
		return ch.Type
	}
	return ch.ID
}

func channelLabel(channelType string) string {
	if label, ok := channelLinkLabels[channelType]; ok {
		return label
	}
	return channelType
}

func sortPublicChannels(channels []publicChannelView) {
	order := make(map[string]int, len(channelLinkOrder))
	for i, id := range channelLinkOrder {
		order[id] = i
	}
	sort.Slice(channels, func(i, j int) bool {
		left, right := channels[i], channels[j]
		leftOrder, leftKnown := order[left.Type]
		rightOrder, rightKnown := order[right.Type]
		if leftKnown && rightKnown && leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		return left.ID < right.ID
	})
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
	var req channelWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.ID = id

	existing, existingErr := s.store.GetChannel(r.Context(), id)
	cfgMap, err := parseChannelConfig(req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON: "+err.Error())
		return
	}

	ch := s.channelFromWriteRequest(r, req, existing, existingErr == nil)
	s.saveChannel(w, r, ch, cfgMap, http.StatusOK)
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var req channelWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	channelType := requestChannelType(req)
	if req.ID == "" || channelType == "" {
		writeError(w, http.StatusBadRequest, "id and type are required")
		return
	}

	cfgMap, err := parseChannelConfig(req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON: "+err.Error())
		return
	}

	ch := config.Channel{
		ID:      req.ID,
		Type:    channelType,
		AgentID: requestAgentID(req),
		Enabled: s.defaultChannelEnabled(r, channelType),
	}
	s.saveChannel(w, r, ch, cfgMap, http.StatusCreated)
}

func requestChannelType(req channelWriteRequest) string {
	if req.Type != nil {
		return *req.Type
	}
	return ""
}

func requestAgentID(req channelWriteRequest) string {
	if req.AgentID != nil {
		return *req.AgentID
	}
	return ""
}

func (s *Server) channelFromWriteRequest(r *http.Request, req channelWriteRequest, existing config.Channel, hasExisting bool) config.Channel {
	channelType := req.ID
	if value := requestChannelType(req); value != "" {
		channelType = value
	} else if hasExisting {
		channelType = effectiveChannelType(existing)
	}

	agentID := requestAgentID(req)
	if req.AgentID == nil && hasExisting {
		agentID = existing.AgentID
	}

	enabled := s.defaultChannelEnabled(r, channelType)
	if hasExisting {
		enabled = existing.Enabled
	}

	return config.Channel{
		ID:      req.ID,
		Type:    channelType,
		AgentID: agentID,
		Enabled: enabled,
	}
}

func (s *Server) defaultChannelEnabled(r *http.Request, channelType string) bool {
	plugin, err := s.store.GetPlugin(r.Context(), config.PluginID(config.PluginKindChannel, channelType))
	return err == nil && plugin.Enabled
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
		pluginEnabled := s.defaultChannelEnabled(r, ch.Type)
		if err := s.store.UpsertPlugin(r.Context(), config.Plugin{
			ID:      pluginID,
			Kind:    config.PluginKindChannel,
			Name:    ch.Type,
			Enabled: pluginEnabled,
			Config:  map[string]any{},
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
