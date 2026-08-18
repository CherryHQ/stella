package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/channel"
)

// Documented page_size ceilings for the group list endpoints. The handler
// rejects a larger page_size with 400 and never passes an unbounded value to the
// boundary, so limit+1 cannot overflow the query layer's int32 columns.
const (
	maxGroupPageSize        = 100
	maxGroupMessagePageSize = 200
)

// resolveGroupPageSize applies the documented default/ceiling to a page_size
// parameter. A nil or non-positive value uses def; a value above max is invalid
// input (ok=false) that the caller maps to 400.
func resolveGroupPageSize(param *int, def, max int) (size int, ok bool) {
	if param == nil || *param <= 0 {
		return def, true
	}
	if *param > max {
		return 0, false
	}
	return *param, true
}

// groupAccess opens one authorized group session for the authenticated caller.
// The Authority carries the verified session role; request path/body fields
// never contribute to it. The group service is a required dependency, so the
// only capability gate is the send path (ErrGroupUnavailable) inside the
// boundary — CRUD/read endpoints are always available.
func (s *Server) groupAccess(w http.ResponseWriter, r *http.Request, info *AuthInfo) (*channel.GroupAccess, bool) {
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	acc, err := s.groupSvc.Begin(r.Context(), authority)
	if err != nil {
		s.groupError(w, err)
		return nil, false
	}
	return acc, true
}

// groupError maps a group domain error to an HTTP response. Group visibility is
// opaque, so a foreign group reports 404; agent-use denials keep the Agent PEP's
// historical bodies.
func (s *Server) groupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, channel.ErrGroupNotFound):
		writeError(w, http.StatusNotFound, "group not found")
	case errors.Is(err, channel.ErrLastGroupMember):
		writeError(w, http.StatusBadRequest, "cannot remove the last member")
	case errors.Is(err, channel.ErrGroupUnavailable):
		writeError(w, http.StatusServiceUnavailable, "group chat not available")
	case errors.Is(err, channel.ErrInvalidPage):
		writeError(w, http.StatusBadRequest, "invalid pagination")
	case errors.Is(err, agentaccess.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent not found")
	case errors.Is(err, agentaccess.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		s.writeInternalError(w, err)
	}
}

func groupToAPI(g channel.Group) apitypes.Group {
	return apitypes.Group{
		Id:                        g.ID,
		GroupName:                 g.GroupName,
		Platform:                  g.Platform,
		CreatedByUserId:           g.CreatedByUserID,
		LastActive:                g.LastActive,
		CreatedAt:                 g.CreatedAt,
		UpdatedAt:                 g.UpdatedAt,
		AgentChainHardLimit:       &g.AgentChainHardLimit,
		MaxAgentPostsPerMinute:    &g.MaxAgentPostsPerMinute,
		MaxRepliesPerHumanTrigger: &g.MaxRepliesPerHumanTrigger,
		HoldLimit:                 &g.HoldLimit,
	}
}

func groupMemberToAPI(m channel.GroupMemberDetail) apitypes.GroupMember {
	return apitypes.GroupMember{
		GroupId:        m.GroupID,
		AgentId:        m.AgentID,
		ReplyChannelId: m.ReplyChannelID,
		AgentName:      m.AgentName,
		CreatedAt:      m.CreatedAt,
	}
}

func groupMessageToAPI(m channel.GroupMessageItem) apitypes.GroupMessage {
	return apitypes.GroupMessage{
		Id:             m.ID,
		GroupId:        m.GroupID,
		Seq:            m.Seq,
		ActorType:      m.ActorType,
		ActorId:        m.ActorID,
		Content:        m.Content,
		Reasoning:      m.Reasoning,
		AgentSessionId: m.AgentSessionID,
		DeliveryState:  &m.DeliveryState,
		CreatedAt:      m.CreatedAt,
	}
}

// StreamGroupEvents replays canonical messages by sequence, then holds a
// best-effort subscription open. Reconnect is the correctness path: the hub
// intentionally drops slow consumers rather than blocking group dispatch.
func (s *Server) StreamGroupEvents(w http.ResponseWriter, r *http.Request, groupId string, params apiserver.StreamGroupEventsParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	since := 0
	if params.SinceSeq != nil {
		if *params.SinceSeq < 0 {
			writeError(w, http.StatusBadRequest, "since_seq must not be negative")
			return
		}
		since = *params.SinceSeq
	}
	rows, err := acc.MessagesAfterSeq(r.Context(), groupId, int64(since))
	if err != nil {
		s.groupError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	events, cancel, err := acc.SubscribeEvents(r.Context(), groupId)
	if err != nil {
		s.groupError(w, err)
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(name string, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		flusher.Flush()
		return err == nil
	}
	for _, row := range rows {
		if !write("message", groupMessageToAPI(row)) {
			return
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, alive := <-events:
			if !alive {
				return
			}
			if event.Turn != nil {
				if !write("turn", event.Turn) {
					return
				}
				continue
			}
			if !write("message", groupMessageToAPI(channel.GroupMessageItem{ID: event.Message.ID, GroupID: event.GroupID, Seq: int(event.Seq), ActorType: event.Message.ActorType, ActorID: event.Message.ActorID, Content: event.Message.Content, DeliveryState: event.Message.DeliveryState, CreatedAt: event.Message.CreatedAt.UTC()})) {
				return
			}
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) ListGroups(w http.ResponseWriter, r *http.Request, params apiserver.ListGroupsParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}

	pageSize, ok := resolveGroupPageSize(params.PageSize, 20, maxGroupPageSize)
	if !ok {
		writeError(w, http.StatusBadRequest, "page_size exceeds maximum")
		return
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}

	groups, err := acc.List(r.Context(), offset, pageSize+1)
	if err != nil {
		s.groupError(w, err)
		return
	}
	page, next := nextPageTokenForRows(groups, pageSize, offset)

	apiGroups := make([]apitypes.Group, len(page))
	for i, g := range page {
		apiGroups[i] = groupToAPI(g)
	}
	out := apitypes.GroupList{Groups: apiGroups}
	if next != "" {
		out.NextPageToken = &next
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) CreateGroup(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}

	var req apitypes.CreateGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.GroupName == "" {
		writeError(w, http.StatusBadRequest, "group_name is required")
		return
	}
	if len(req.AgentIds) == 0 {
		writeError(w, http.StatusBadRequest, "at least one agent_id is required")
		return
	}

	g, err := acc.Create(r.Context(), req.GroupName, req.AgentIds)
	if err != nil {
		s.groupError(w, err)
		return
	}
	writeData(w, http.StatusCreated, groupToAPI(g))
}

func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	g, err := acc.Get(r.Context(), groupId)
	if err != nil {
		s.groupError(w, err)
		return
	}
	writeData(w, http.StatusOK, groupToAPI(g))
}

func (s *Server) UpdateGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	var req apitypes.UpdateGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var g channel.Group
	var err error
	hasCaps := req.AgentChainHardLimit != nil || req.MaxAgentPostsPerMinute != nil || req.MaxRepliesPerHumanTrigger != nil || req.HoldLimit != nil
	switch {
	case hasCaps:
		current, getErr := acc.Get(r.Context(), groupId)
		if getErr != nil {
			s.groupError(w, getErr)
			return
		}
		caps := channel.GroupDispatchCaps{AgentChainHardLimit: current.AgentChainHardLimit, MaxAgentPostsPerMinute: current.MaxAgentPostsPerMinute, MaxRepliesPerHumanTrigger: current.MaxRepliesPerHumanTrigger, HoldLimit: current.HoldLimit}
		if req.AgentChainHardLimit != nil {
			caps.AgentChainHardLimit = *req.AgentChainHardLimit
		}
		if req.MaxAgentPostsPerMinute != nil {
			caps.MaxAgentPostsPerMinute = *req.MaxAgentPostsPerMinute
		}
		if req.MaxRepliesPerHumanTrigger != nil {
			caps.MaxRepliesPerHumanTrigger = *req.MaxRepliesPerHumanTrigger
		}
		if req.HoldLimit != nil {
			caps.HoldLimit = *req.HoldLimit
		}
		g, err = acc.UpdateCaps(r.Context(), groupId, caps)
	case req.GroupName != nil && *req.GroupName != "":
		g, err = acc.UpdateName(r.Context(), groupId, *req.GroupName)
	default:
		writeError(w, http.StatusBadRequest, "at least one field is required")
		return
	}
	if err != nil {
		s.groupError(w, err)
		return
	}
	writeData(w, http.StatusOK, groupToAPI(g))
}

func (s *Server) DeleteGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	if err := acc.Delete(r.Context(), groupId); err != nil {
		s.groupError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ListGroupMembers(w http.ResponseWriter, r *http.Request, groupId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	members, err := acc.Members(r.Context(), groupId)
	if err != nil {
		s.groupError(w, err)
		return
	}
	apiMembers := make([]apitypes.GroupMember, len(members))
	for i, m := range members {
		apiMembers[i] = groupMemberToAPI(m)
	}
	writeData(w, http.StatusOK, apitypes.GroupMemberList{Members: apiMembers})
}

func (s *Server) AddGroupMember(w http.ResponseWriter, r *http.Request, groupId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	var req apitypes.AddGroupMemberRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	m, err := acc.AddMember(r.Context(), groupId, req.AgentId)
	if err != nil {
		s.groupError(w, err)
		return
	}
	writeData(w, http.StatusCreated, groupMemberToAPI(m))
}

func (s *Server) RemoveGroupMember(w http.ResponseWriter, r *http.Request, groupId string, agentId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}
	if err := acc.RemoveMember(r.Context(), groupId, agentId); err != nil {
		s.groupError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ListGroupMessages(w http.ResponseWriter, r *http.Request, groupId string, params apiserver.ListGroupMessagesParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}

	pageSize, ok := resolveGroupPageSize(params.PageSize, 50, maxGroupMessagePageSize)
	if !ok {
		writeError(w, http.StatusBadRequest, "page_size exceeds maximum")
		return
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}

	msgs, err := acc.Messages(r.Context(), groupId, offset, pageSize+1)
	if err != nil {
		s.groupError(w, err)
		return
	}
	page, next := nextPageTokenForRows(msgs, pageSize, offset)

	apiMsgs := make([]apitypes.GroupMessage, len(page))
	for i, m := range page {
		apiMsgs[i] = groupMessageToAPI(m)
	}
	out := apitypes.GroupMessageList{Messages: apiMsgs}
	if next != "" {
		out.NextPageToken = &next
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) SendGroupMessage(w http.ResponseWriter, r *http.Request, groupId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	acc, ok := s.groupAccess(w, r, info)
	if !ok {
		return
	}

	var req apitypes.SendGroupMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	// The group boundary authorizes ownership, intercepts group commands, and
	// appends+claims the outbox in one transaction (dedup preserved). The transport
	// only decides how to render each of the three outcomes as SSE.
	prep, err := acc.PrepareSend(r.Context(), groupId, req.Content, derefStr(req.ClientMessageId))
	if err != nil {
		s.groupError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	if prep.Command {
		streamPlainReply(w, flusher, prep.Reply)
		return
	}
	if prep.Deduplicated {
		streamEmptyGroupReply(w, flusher)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	publisher := &webGroupPublisher{w: w, flusher: flusher}
	publisher.writeSSE(map[string]string{"type": "start", "messageId": uuid.Must(uuid.NewV7()).String()})
	// s.runtimeCtx (not the request/drain context) parents the dispatch: the group
	// turn runs here, so a drain-start cancellation would kill in-flight work the
	// graceful HTTP shutdown budget exists to finish. The boundary bounds the turn
	// by the outbox lease.
	if err := acc.Dispatch(s.runtimeCtx, prep, publisher); err != nil {
		publisher.writeSSE(map[string]string{"type": "error", "errorText": err.Error()})
	}
	publisher.writeSSE(map[string]string{"type": "finish"})
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}
