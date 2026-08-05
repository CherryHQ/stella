package server

import (
	"encoding/json"
	"net/http"
	"sort"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
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
	// Config is tri-state like AgentID: nil keeps the stored config, an explicit
	// "" or "{}" clears it, and a non-empty JSON object replaces it.
	Config *string `json:"config"`
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
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	enabledTypes, err := s.controlPlane.EnabledChannelTypes(r.Context(), authority)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}

	agentNames, err := s.accessibleAgentNames(r, info)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	channels, err := s.controlPlane.PublicChannels(r.Context(), authority)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}

	writeData(w, http.StatusOK, map[string]any{"channels": buildPublicChannelViews(channels, enabledTypes, agentNames)})
}

func (s *Server) accessibleAgentNames(r *http.Request, info *AuthInfo) (map[string]string, error) {
	ctx := r.Context()
	authority, err := info.authority()
	if err != nil {
		return nil, agentaccess.ErrForbidden
	}
	agents, err := s.agentAccess.ListReadable(ctx, authority, info.IsAdmin)
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

func buildPublicChannelViews(channels []controlplane.PublicChannel, enabledTypes map[string]bool, agentNames map[string]string) []publicChannelView {
	views := make([]publicChannelView, 0, len(channels))
	for _, ch := range channels {
		channelType := ch.Type
		if !ch.Enabled || !enabledTypes[channelType] {
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

// BindChannelAgent points a channel at an agent, or unbinds it when agent_id is
// empty. It is the one channel write open to a non-admin, so it is deliberately
// separate from the admin PATCH: it never accepts (or returns) the channel
// config, and it answers with the same secret-free projection the public channel
// list serves.
func (s *Server) BindChannelAgent(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx := r.Context()
	binding, err := s.controlPlane.BeginChannelBinding(ctx, authority, id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	// A non-admin may only bind an agent they can use, and may only take a channel
	// away from an agent they can use — a channel they cannot reach is not theirs
	// to repoint. Both are Agent PEP decisions, made before the channel write.
	if !info.IsAdmin {
		for _, agentID := range []string{binding.CurrentAgentID(), body.AgentID} {
			if agentID == "" {
				continue
			}
			if _, code, msg := s.requireAgentUse(ctx, agentID); code != 0 {
				writeError(w, code, msg)
				return
			}
		}
	}

	saved, err := binding.Bind(ctx, body.AgentID)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	agentNames, err := s.accessibleAgentNames(r, info)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, publicChannelView{
		ID:        saved.ID,
		Type:      saved.Type,
		Label:     channelLabel(saved.Type),
		AgentID:   saved.AgentID,
		AgentName: agentNames[saved.AgentID],
		Enabled:   saved.Enabled,
	})
}

func (s *Server) ListChannels(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	channels, err := access.ListChannels(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	views := make([]channelView, len(channels))
	for i, ch := range channels {
		views[i] = channelToView(ch)
	}
	writeData(w, http.StatusOK, map[string]any{"channels": views})
}

func (s *Server) GetChannel(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	ch, err := access.GetChannel(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, channelToView(ch))
}

func (s *Server) UpdateChannel(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req channelWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.ID = id

	ctx := r.Context()
	// Load the current row only to merge unspecified write fields (a PUT is a
	// partial update). The read goes through the authorized Access — beginControlPlane
	// already 403'd a non-admin before any state is observed — so the transport never
	// holds the aggregate config store.
	existing, existingErr := access.GetChannel(ctx, id)
	hasExisting := existingErr == nil
	// Save overwrites the stored config unconditionally, so an omitted config must
	// resolve to the current row's config or a PATCH of an unrelated field would
	// wipe the channel's credentials.
	cfgMap, err := parseChannelConfig(requestConfig(req, existing, hasExisting))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON")
		return
	}

	ch := s.channelFromWriteRequest(r, req, existing, hasExisting)
	saved, err := access.SaveChannel(ctx, ch, cfgMap, false)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, channelToView(saved))
}

func (s *Server) CreateChannel(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
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

	cfgMap, err := parseChannelConfig(requestConfig(req, config.Channel{}, false))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config JSON")
		return
	}

	ch := config.Channel{
		ID:      req.ID,
		Name:    req.Name,
		Type:    channelType,
		AgentID: requestAgentID(req),
		Enabled: false,
	}
	// create=true enforces the POST create-only contract inside the PEP (after
	// authorization): a re-POST to an existing id is a 409, never a silent upsert.
	saved, err := access.SaveChannel(r.Context(), ch, cfgMap, true)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusCreated, channelToView(saved))
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

// requestConfig resolves the raw config JSON a write request should persist:
// an absent config keeps the existing row's config, an explicit value replaces it.
func requestConfig(req channelWriteRequest, existing config.Channel, hasExisting bool) string {
	if req.Config != nil {
		return *req.Config
	}
	if hasExisting {
		return existing.Config
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
	}

	return config.Channel{
		ID:      req.ID,
		Name:    name,
		Type:    channelType,
		AgentID: agentID,
		Enabled: enabled,
	}
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
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.DeleteChannel(r.Context(), id); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeNoContent(w)
}
