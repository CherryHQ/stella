package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// channelView is the JSON shape the admin frontend expects for channel objects.
// Config is serialized as a JSON string for the admin frontend.
type channelView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
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
	Name    string  `json:"name"`
	Type    *string `json:"type"`
	AgentID *string `json:"agent_id"`
	Enabled *bool   `json:"enabled"`
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
		Name:    ch.Name,
		Type:    ch.Type,
		AgentID: ch.AgentID,
		Enabled: ch.Enabled,
		Config:  ch.Config,
	}
}

func (s *Server) ListPublicChannels(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}

	enabledTypes, err := s.enabledChannelTypes(r)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	agentNames, err := s.accessibleAgentNames(r, info)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeData(w, http.StatusOK, map[string]any{"channels": buildPublicChannelViews(channels, enabledTypes, agentNames)})
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

func (s *Server) accessibleAgentNames(r *http.Request, info *AuthInfo) (map[string]string, error) {
	ctx := r.Context()
	var agents []config.Agent
	var err error
	if info.IsAdmin {
		agents, err = s.store.ListAgents(ctx)
	} else {
		agents, err = s.store.ListAccessibleAgents(ctx, info.UserID)
	}
	if err != nil {
		return nil, err
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
		if !ch.Enabled || !enabledTypes[channelType] {
			continue
		}
		// Webhooks are inbound triggers with no linkable identity; they never
		// belong in the user-facing channel list (the link-code flow rejects
		// them, and the list keys by type so duplicates would collide).
		// Upgrade to a plugin capability flag when the next runtime-less
		// channel type lands.
		if channelType == pkgchannel.PlatformWebhook {
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

func (s *Server) ListChannels(w http.ResponseWriter, r *http.Request) {
	info := requireAdmin(w, r)
	if info == nil {
		return
	}
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	views := make([]channelView, len(channels))
	for i, ch := range channels {
		views[i] = channelToView(ch)
	}
	writeData(w, http.StatusOK, map[string]any{"channels": views})
}

func (s *Server) GetChannel(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	ch, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeData(w, http.StatusOK, channelToView(ch))
}

func (s *Server) UpdateChannel(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	var req channelWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.ID = id

	ctx := r.Context()
	existing, existingErr := s.store.GetChannel(ctx, id)
	cfgMap, err := parseChannelConfig(req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON")
		return
	}

	ch := s.channelFromWriteRequest(r, req, existing, existingErr == nil)
	s.saveChannel(w, r, ch, cfgMap, http.StatusOK)
}

func (s *Server) CreateChannel(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var req channelWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	channelType := requestChannelType(req)
	if req.ID == "" || channelType == "" {
		writeError(w, http.StatusBadRequest, "id and type are required")
		return
	}
	if channelType == pkgchannel.PlatformWeixin {
		if err := validateWeixinChannelID(req.ID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// POST is create-only: silently upserting would let a re-POST overwrite an
	// existing channel's config and flip a deliberately disabled webhook back on.
	if _, err := s.store.GetChannel(r.Context(), req.ID); err == nil {
		writeError(w, http.StatusConflict, "channel already exists")
		return
	}

	cfgMap, err := parseChannelConfig(req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON")
		return
	}

	ch := config.Channel{
		ID:      req.ID,
		Name:    req.Name,
		Type:    channelType,
		AgentID: requestAgentID(req),
		// Bot channels start disabled until their runtime is configured/scanned;
		// a webhook has no runtime, so it goes live the moment it's created.
		Enabled: channelType == pkgchannel.PlatformWebhook,
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

	name := req.Name
	if name == "" && hasExisting {
		name = existing.Name
	}

	enabled := false
	switch {
	case req.Enabled != nil:
		enabled = *req.Enabled
	case hasExisting:
		enabled = existing.Enabled
	case channelType == pkgchannel.PlatformWebhook:
		// PUT-created webhooks match POST semantics: no runtime to configure,
		// so they go live on creation unless explicitly disabled.
		enabled = true
	}

	return config.Channel{
		ID:      req.ID,
		Name:    name,
		Type:    channelType,
		AgentID: agentID,
		Enabled: enabled,
	}
}

func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request, ch config.Channel, cfgMap map[string]any, status int) bool {
	// A webhook is a runtime-less trigger: it must name the agent it runs, but
	// its caller is resolved dynamically from the PAT (not bound to one user).
	if ch.Type == pkgchannel.PlatformWebhook && ch.AgentID == "" {
		writeError(w, http.StatusBadRequest, "webhook channel requires a bound agent")
		return false
	}
	if conflict, err := s.channelAgentPlatformBindingConflict(r.Context(), ch); err != nil {
		s.writeInternalError(w, err)
		return false
	} else if conflict != "" {
		writeError(w, http.StatusBadRequest, conflict)
		return false
	}
	pluginID := config.PluginID(config.PluginKindChannel, ch.Type)
	if err := s.pluginHost.ValidateConfig(pluginID, cfgMap); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	cfgJSON, err := json.Marshal(cfgMap)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON")
		return false
	}
	ch.Config = string(cfgJSON)
	if err := s.store.UpsertChannel(r.Context(), ch); err != nil {
		s.writeInternalError(w, err)
		return false
	}
	if err := s.ensureChannelPluginEnabled(r.Context(), ch.Type); err != nil {
		s.writeInternalError(w, err)
		return false
	}
	if err := s.pluginHost.ApplyChannel(r.Context(), ch); err != nil {
		s.log.Error("failed to apply channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
	}
	saved, err := s.store.GetChannel(r.Context(), ch.ID)
	if err != nil {
		s.writeInternalError(w, err)
		return false
	}
	writeData(w, status, channelToView(saved))
	return true
}

func (s *Server) channelAgentPlatformBindingConflict(ctx context.Context, ch config.Channel) (string, error) {
	if ch.AgentID == "" || ch.Type == "" {
		return "", nil
	}
	// Webhooks are inbound triggers, not a single bidirectional binding: an agent
	// may back many webhook endpoints, so the one-per-agent rule does not apply.
	if ch.Type == pkgchannel.PlatformWebhook {
		return "", nil
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return "", err
	}
	for _, existing := range channels {
		if existing.ID == ch.ID {
			continue
		}
		if effectiveChannelType(existing) == ch.Type && existing.AgentID == ch.AgentID {
			return "agent is already bound to " + ch.Type + " channel " + existing.ID, nil
		}
	}
	return "", nil
}

func (s *Server) ensureChannelPluginEnabled(ctx context.Context, channelType string) error {
	pluginID := config.PluginID(config.PluginKindChannel, channelType)
	return s.store.UpsertPlugin(ctx, config.Plugin{
		ID:      pluginID,
		Kind:    config.PluginKindChannel,
		Name:    channelType,
		Enabled: true,
		Config:  map[string]any{},
	})
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

func (s *Server) DeleteChannel(w http.ResponseWriter, r *http.Request, id string) {
	if requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	ch, err := s.store.GetChannel(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	ch.Enabled = false
	if err := s.pluginHost.ApplyChannel(ctx, ch); err != nil {
		s.log.Error("failed to stop channel runtime", "channel_id", ch.ID, "channel_type", ch.Type, "error", err)
	}
	if err := s.store.DeleteChannel(ctx, id); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}
