package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func webChannelID(agentID string) string { return "web:" + agentID }

func groupToAPI(g sqlc.CtxGroupState) apitypes.Group {
	resp := apitypes.Group{
		Id:        g.ID,
		GroupName: g.GroupName,
		Platform:  g.Platform,
		CreatedAt: parseTime(g.CreatedAt),
		UpdatedAt: parseTime(g.UpdatedAt),
	}
	if g.CreatedByUserID.Valid {
		resp.CreatedByUserId = &g.CreatedByUserID.String
	}
	return resp
}

func (s *Server) requireGroupOwner(w http.ResponseWriter, r *http.Request, groupID string) (sqlc.CtxGroupState, bool) {
	info := requireAuth(w, r)
	if info == nil {
		return sqlc.CtxGroupState{}, false
	}
	g, err := s.q.GetGroupStateByID(r.Context(), groupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "group not found")
		} else {
			s.writeInternalError(w, err)
		}
		return sqlc.CtxGroupState{}, false
	}
	if !info.IsAdmin && (!g.CreatedByUserID.Valid || g.CreatedByUserID.String != info.UserID) {
		writeError(w, http.StatusNotFound, "group not found")
		return sqlc.CtxGroupState{}, false
	}
	return g, true
}

func (s *Server) ListGroups(w http.ResponseWriter, r *http.Request, params apiserver.ListGroupsParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	ctx := r.Context()

	pageSize := int64(20)
	if params.PageSize != nil && *params.PageSize > 0 {
		pageSize = int64(*params.PageSize)
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}

	groups, err := s.q.ListGroupsByUser(ctx, sqlc.ListGroupsByUserParams{
		UserID:      sql.NullString{String: info.UserID, Valid: true},
		LimitCount:  pageSize + 1,
		OffsetCount: int64(offset),
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	var nextPageToken *string
	if int64(len(groups)) > pageSize {
		groups = groups[:pageSize]
		tok := encodeOffsetToken(offset + int(pageSize))
		nextPageToken = &tok
	}

	apiGroups := make([]apitypes.Group, len(groups))
	for i, g := range groups {
		apiGroups[i] = groupToAPI(g)
	}

	writeData(w, http.StatusOK, apitypes.GroupList{
		Groups:        apiGroups,
		NextPageToken: nextPageToken,
	})
}

func (s *Server) CreateGroup(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	ctx := r.Context()

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

	for _, agentID := range req.AgentIds {
		if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
			writeError(w, code, msg)
			return
		}
	}

	groupID := uuid.NewString()
	g, err := s.q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               groupID,
		Platform:         "web",
		PlatformGroupID:  groupID,
		PlatformThreadID: "",
		GroupName:        req.GroupName,
		CreatedByUserID:  sql.NullString{String: info.UserID, Valid: true},
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	for _, agentID := range req.AgentIds {
		if err := s.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
			ID:      webChannelID(agentID),
			AgentID: sql.NullString{String: agentID, Valid: true},
		}); err != nil {
			s.writeInternalError(w, err)
			return
		}
		if _, err := s.q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
			GroupID:        groupID,
			AgentID:        agentID,
			ReplyChannelID: webChannelID(agentID),
		}); err != nil {
			s.writeInternalError(w, err)
			return
		}
	}

	writeData(w, http.StatusCreated, groupToAPI(g))
}

func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	g, ok := s.requireGroupOwner(w, r, groupId)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, groupToAPI(g))
}

func (s *Server) UpdateGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	if _, ok := s.requireGroupOwner(w, r, groupId); !ok {
		return
	}
	var req apitypes.UpdateGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.GroupName == "" {
		writeError(w, http.StatusBadRequest, "group_name is required")
		return
	}
	g, err := s.q.UpdateGroupName(r.Context(), sqlc.UpdateGroupNameParams{
		GroupName: req.GroupName,
		ID:        groupId,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, groupToAPI(g))
}

func (s *Server) DeleteGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	if _, ok := s.requireGroupOwner(w, r, groupId); !ok {
		return
	}
	if err := s.q.DeleteGroupState(r.Context(), groupId); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ListGroupMembers(w http.ResponseWriter, r *http.Request, groupId string) {
	if _, ok := s.requireGroupOwner(w, r, groupId); !ok {
		return
	}
	ctx := r.Context()
	members, err := s.q.ListGroupMembers(ctx, groupId)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	apiMembers := make([]apitypes.GroupMember, len(members))
	for i, m := range members {
		apiMembers[i] = apitypes.GroupMember{
			GroupId:        m.GroupID,
			AgentId:        m.AgentID,
			ReplyChannelId: m.ReplyChannelID,
			CreatedAt:      parseTime(m.CreatedAt),
		}
		if a, err := s.store.GetAgent(ctx, m.AgentID); err == nil {
			apiMembers[i].AgentName = &a.Name
		}
	}

	writeData(w, http.StatusOK, apitypes.GroupMemberList{Members: apiMembers})
}

func (s *Server) AddGroupMember(w http.ResponseWriter, r *http.Request, groupId string) {
	if _, ok := s.requireGroupOwner(w, r, groupId); !ok {
		return
	}
	ctx := r.Context()

	var req apitypes.AddGroupMemberRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, code, msg := s.requireAgentAccess(ctx, req.AgentId); code != 0 {
		writeError(w, code, msg)
		return
	}

	if err := s.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      webChannelID(req.AgentId),
		AgentID: sql.NullString{String: req.AgentId, Valid: true},
	}); err != nil {
		s.writeInternalError(w, err)
		return
	}
	m, err := s.q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        groupId,
		AgentID:        req.AgentId,
		ReplyChannelID: webChannelID(req.AgentId),
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	resp := apitypes.GroupMember{
		GroupId:        m.GroupID,
		AgentId:        m.AgentID,
		ReplyChannelId: m.ReplyChannelID,
		CreatedAt:      parseTime(m.CreatedAt),
	}
	if a, aErr := s.store.GetAgent(ctx, m.AgentID); aErr == nil {
		resp.AgentName = &a.Name
	}
	writeData(w, http.StatusCreated, resp)
}

func (s *Server) RemoveGroupMember(w http.ResponseWriter, r *http.Request, groupId string, agentId string) {
	if _, ok := s.requireGroupOwner(w, r, groupId); !ok {
		return
	}
	ctx := r.Context()

	count, err := s.q.CountGroupMembers(ctx, groupId)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	if count <= 1 {
		writeError(w, http.StatusBadRequest, "cannot remove the last member")
		return
	}

	if err := s.q.RemoveGroupMember(ctx, sqlc.RemoveGroupMemberParams{
		GroupID: groupId,
		AgentID: agentId,
	}); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ListGroupMessages(w http.ResponseWriter, r *http.Request, groupId string, params apiserver.ListGroupMessagesParams) {
	if _, ok := s.requireGroupOwner(w, r, groupId); !ok {
		return
	}
	ctx := r.Context()

	pageSize := int64(50)
	if params.PageSize != nil && *params.PageSize > 0 {
		pageSize = int64(*params.PageSize)
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}

	msgs, err := s.q.ListGroupMessagesPaginated(ctx, sqlc.ListGroupMessagesPaginatedParams{
		GroupID:     groupId,
		LimitCount:  pageSize + 1,
		OffsetCount: int64(offset),
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	var nextPageToken *string
	if int64(len(msgs)) > pageSize {
		msgs = msgs[:pageSize]
		tok := encodeOffsetToken(offset + int(pageSize))
		nextPageToken = &tok
	}

	apiMsgs := make([]apitypes.GroupMessage, len(msgs))
	for i, m := range msgs {
		apiMsgs[i] = apitypes.GroupMessage{
			Id:             m.ID,
			GroupId:        m.GroupID,
			Seq:            int(m.Seq),
			ActorType:      m.ActorType,
			ActorId:        m.ActorID,
			Content:        m.Content,
			Reasoning:      ptrStr(m.Reasoning),
			AgentSessionId: ptrStr(m.AgentSessionID),
			CreatedAt:      parseTime(m.CreatedAt),
		}
	}

	writeData(w, http.StatusOK, apitypes.GroupMessageList{
		Messages:      apiMsgs,
		NextPageToken: nextPageToken,
	})
}

func (s *Server) SendGroupMessage(w http.ResponseWriter, r *http.Request, groupId string) {
	if s.eventLog == nil {
		writeError(w, http.StatusServiceUnavailable, "event log not available")
		return
	}

	g, ok := s.requireGroupOwner(w, r, groupId)
	if !ok {
		return
	}
	_ = g

	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
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

	ctx := r.Context()

	members, err := s.q.ListGroupMembers(ctx, groupId)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	// Intercept slash commands before they hit the event log + LLM.
	if reply, handled := s.handleGroupCommand(ctx, groupId, req.Content, members); handled {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		streamPlainReply(w, flusher, reply)
		return
	}

	appendResult, err := s.eventLog.AppendToGroup(ctx, groupId, eventlog.GroupMessage{
		ActorType: eventlog.ActorHuman,
		ActorID:   authInfo.UserID,
		Content:   req.Content,
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	// Determine responding agents: @mention → mentioned only, else → all members.
	responding := resolveGroupResponders(req.Content, members)
	if len(responding) == 0 {
		writeError(w, http.StatusBadRequest, "no responding agents")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeSSE := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	writeDone := func() {
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	writeSSE(map[string]string{"type": "start", "messageId": uuid.NewString()})

	baseCtx := memory.WithUserID(ctx, authInfo.UserID)
	baseCtx = memory.WithGroupSeq(baseCtx, appendResult.Seq)

	for _, agentID := range responding {
		writeSSE(map[string]string{"type": "start-step"})
		s.streamAgentResponse(baseCtx, writeSSE, groupId, agentID, req.Content)
		writeSSE(map[string]string{"type": "finish-step"})
	}

	writeSSE(map[string]string{"type": "finish"})
	writeDone()
}

func (s *Server) streamAgentResponse(
	baseCtx context.Context,
	writeSSE func(any),
	groupID, agentID string,
	messageContent string,
) {
	svc := s.poolManager.GetService(agentID)
	if svc == nil {
		writeSSE(map[string]string{"type": "error", "errorText": "agent not available: " + agentID})
		return
	}

	agentCtx := memory.WithAgentID(baseCtx, agentID)

	sessionKey := agent.BuildGroupSessionKey(agentID, groupID)
	channelStr := "group:" + groupID

	info, err := svc.ResolveChannelSession(agentCtx, sessionKey, groupID, agentID, session.Channel(channelStr))
	if err != nil {
		writeSSE(map[string]string{"type": "error", "errorText": "session error: " + err.Error()})
		return
	}
	info.GroupID = groupID

	agentName := agentID
	if a, aErr := s.store.GetAgent(agentCtx, agentID); aErr == nil && a.Name != "" {
		agentName = a.Name
	}

	// Emit agent identity as a data part (consumed by useChat as a data-* UIMessage part).
	writeSSE(map[string]any{
		"type": "data-agent-info",
		"id":   uuid.NewString(),
		"data": map[string]any{"agentId": agentID, "agentName": agentName},
	})

	var (
		inText       bool
		textID       string
		inReasoning  bool
		reasoningID  string
		textBuf      strings.Builder
		reasoningBuf strings.Builder
	)

	closeText := func() {
		if inText {
			writeSSE(map[string]string{"type": "text-end", "id": textID})
			inText = false
		}
	}
	closeReasoning := func() {
		if inReasoning {
			writeSSE(map[string]string{"type": "reasoning-end", "id": reasoningID})
			inReasoning = false
		}
	}

	ch := svc.Chat(agentCtx, agent.ChatRequest{
		SessionID: info.ID,
		UserID:    groupID,
		AgentID:   agentID,
		Kind:      session.KindChat,
		GroupID:   groupID,
		Channel:   session.Channel(channelStr),
		Message:   messageContent,
	})
	for evt := range ch {
		if evt.Err != nil {
			closeText()
			closeReasoning()
			writeSSE(map[string]string{"type": "error", "errorText": evt.Err.Error()})
			break
		}
		if evt.Store != nil || evt.Step != nil {
			continue
		}
		if evt.Reasoning != "" {
			closeText()
			if !inReasoning {
				reasoningID = uuid.NewString()
				writeSSE(map[string]string{"type": "reasoning-start", "id": reasoningID})
				inReasoning = true
			}
			reasoningBuf.WriteString(evt.Reasoning)
			writeSSE(map[string]any{"type": "reasoning-delta", "id": reasoningID, "delta": evt.Reasoning})
			continue
		}
		if evt.ToolUse != nil {
			closeText()
			closeReasoning()
			tu := evt.ToolUse
			switch tu.Status {
			case "running":
				writeSSE(map[string]any{
					"type":       "tool-input-start",
					"toolCallId": tu.ID,
					"toolName":   tu.Tool,
					"dynamic":    true,
				})
				args := tu.Arguments
				if args == nil {
					args = map[string]any{"input": tu.Input}
				}
				writeSSE(map[string]any{
					"type":       "tool-input-available",
					"toolCallId": tu.ID,
					"toolName":   tu.Tool,
					"dynamic":    true,
					"input":      args,
				})
			case "done":
				writeSSE(map[string]any{
					"type":       "tool-output-available",
					"toolCallId": tu.ID,
					"output":     tu.Content,
				})
			case "error":
				writeSSE(map[string]any{
					"type":       "tool-output-error",
					"toolCallId": tu.ID,
					"errorText":  tu.Content,
				})
			}
			continue
		}
		if evt.Text != "" {
			closeReasoning()
			if !inText {
				textID = uuid.NewString()
				writeSSE(map[string]string{"type": "text-start", "id": textID})
				inText = true
			}
			textBuf.WriteString(evt.Text)
			writeSSE(map[string]any{"type": "text-delta", "id": textID, "delta": evt.Text})
			continue
		}
	}

	closeText()
	closeReasoning()

	// Write agent response to event log.
	if (textBuf.Len() > 0 || reasoningBuf.Len() > 0) && s.eventLog != nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(baseCtx), 10*time.Second)
		defer cancel()
		_, _ = s.eventLog.AppendToGroup(writeCtx, groupID, eventlog.GroupMessage{
			ActorType:      eventlog.ActorAgent,
			ActorID:        agentID,
			Content:        textBuf.String(),
			Reasoning:      reasoningBuf.String(),
			AgentSessionID: info.ID,
		})
	}
}

// handleGroupCommand intercepts slash commands in group chat.
// Returns the reply text and true if handled.
func (s *Server) handleGroupCommand(ctx context.Context, groupID, content string, members []sqlc.ChannelGroupMember) (string, bool) {
	cmd, ok := parseCommand(content)
	if !ok {
		return "", false
	}
	switch cmd {
	case "/compact":
		var results []string
		for _, m := range members {
			svc := s.poolManager.GetService(m.AgentID)
			if svc == nil {
				continue
			}
			agentCtx := memory.WithAgentID(ctx, m.AgentID)
			sessionKey := agent.BuildGroupSessionKey(m.AgentID, groupID)
			channelStr := "group:" + groupID
			info, err := svc.ResolveChannelSession(agentCtx, sessionKey, groupID, m.AgentID, session.Channel(channelStr))
			if err != nil {
				results = append(results, fmt.Sprintf("%s: failed to resolve session: %v", m.AgentID, err))
				continue
			}
			info.GroupID = groupID
			summary, err := svc.CompactSession(agentCtx, info)
			if err != nil {
				results = append(results, fmt.Sprintf("%s: compaction failed: %v", m.AgentID, err))
				continue
			}
			results = append(results, fmt.Sprintf("%s: %s", m.AgentID, summary))
		}
		if len(results) == 0 {
			return "No agents to compact.", true
		}
		return strings.Join(results, "\n"), true
	default:
		return "", false
	}
}

// resolveGroupResponders determines which agents should respond.
// If @AgentName patterns are found in the content, only matched agents respond.
// Otherwise, all members respond.
func resolveGroupResponders(content string, members []sqlc.ChannelGroupMember) []string {
	// Simple @mention parsing: extract @word patterns and match against member agent IDs.
	var mentioned []string
	words := strings.Fields(content)
	memberIDs := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberIDs[m.AgentID] = struct{}{}
	}
	for _, w := range words {
		if after, ok := strings.CutPrefix(w, "@"); ok {
			name := after
			if _, ok := memberIDs[name]; ok {
				mentioned = append(mentioned, name)
			}
		}
	}
	if len(mentioned) > 0 {
		return mentioned
	}
	all := make([]string, len(members))
	for i, m := range members {
		all[i] = m.AgentID
	}
	return all
}
