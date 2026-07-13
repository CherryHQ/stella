package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func webChannelID(agentID string) string { return "web:" + agentID }

const groupOutboxLeaseDuration = 5 * time.Minute

func groupToAPI(g sqlc.CtxGroupState) apitypes.Group {
	resp := apitypes.Group{
		Id:        g.ID,
		GroupName: g.GroupName,
		Platform:  g.Platform,
		CreatedAt: g.CreatedAt.UTC(),
		UpdatedAt: g.UpdatedAt.UTC(),
	}
	resp.LastActive = &resp.UpdatedAt
	if g.CreatedByUserID.Valid {
		resp.CreatedByUserId = &g.CreatedByUserID.String
	}
	return resp
}

// groupToAPIWithActivity keeps single-resource responses on the same
// message-derived last_active semantics as the list endpoint.
func (s *Server) groupToAPIWithActivity(ctx context.Context, g sqlc.CtxGroupState) apitypes.Group {
	resp := groupToAPI(g)
	if la, err := s.q.GetGroupLastActive(ctx, g.ID); err == nil {
		if t := la.UTC(); !t.IsZero() {
			resp.LastActive = &t
		}
	}
	return resp
}

func groupListRowToAPI(g sqlc.ListGroupsByUserRow) apitypes.Group {
	resp := apitypes.Group{
		Id:        g.ID,
		GroupName: g.GroupName,
		Platform:  g.Platform,
		CreatedAt: g.CreatedAt.UTC(),
		UpdatedAt: g.UpdatedAt.UTC(),
	}
	lastActive := g.LastActive.UTC()
	if !lastActive.IsZero() {
		resp.LastActive = &lastActive
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
		if isNotFound(err) {
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
		UserID:      pgtype.Text{String: info.UserID, Valid: true},
		LimitCount:  int32(pageSize + 1),
		OffsetCount: int32(offset),
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
		apiGroups[i] = groupListRowToAPI(g)
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

	groupID := uuid.Must(uuid.NewV7()).String()
	g, err := s.q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               groupID,
		Platform:         "web",
		PlatformGroupID:  groupID,
		PlatformThreadID: "",
		GroupName:        req.GroupName,
		CreatedByUserID:  pgtype.Text{String: info.UserID, Valid: true},
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	for _, agentID := range req.AgentIds {
		if err := s.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
			ID:      webChannelID(agentID),
			AgentID: pgtype.Text{String: agentID, Valid: true},
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

	writeData(w, http.StatusCreated, s.groupToAPIWithActivity(r.Context(), g))
}

func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request, groupId string) {
	g, ok := s.requireGroupOwner(w, r, groupId)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, s.groupToAPIWithActivity(r.Context(), g))
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
	writeData(w, http.StatusOK, s.groupToAPIWithActivity(r.Context(), g))
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
			CreatedAt:      m.CreatedAt.UTC(),
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
		AgentID: pgtype.Text{String: req.AgentId, Valid: true},
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
		CreatedAt:      m.CreatedAt.UTC(),
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
		LimitCount:  int32(pageSize + 1),
		OffsetCount: int32(offset),
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
			CreatedAt:      m.CreatedAt.UTC(),
		}
	}

	writeData(w, http.StatusOK, apitypes.GroupMessageList{
		Messages:      apiMsgs,
		NextPageToken: nextPageToken,
	})
}

func (s *Server) SendGroupMessage(w http.ResponseWriter, r *http.Request, groupId string) {
	if s.eventLog == nil || s.groupDispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "group chat not available")
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

	// Unified entry: triple resolve + dedup via AppendGroupMessage.
	// Empty client_message_id disables tier-1 dedup (no fake UUID).
	platformMsgID := derefStr(req.ClientMessageId)
	appendResult, err := s.eventLog.AppendGroupMessage(ctx, eventlog.Message{
		Platform:         "web",
		PlatformGroupID:  groupId,
		PlatformThreadID: "",
		ActorType:        eventlog.ActorHuman,
		// Invariant (#308): the Web group actor_id is the authenticated user id.
		// The group dispatcher (webGroupSpeaker) trusts this as the current-speaker
		// profile target, so it must never be a client-supplied or otherwise
		// unauthenticated value.
		ActorID:           authInfo.UserID,
		PlatformMessageID: platformMsgID,
		Content:           req.Content,
	}, eventlog.WithOnInserted(func(ctx context.Context, q *sqlc.Queries, result eventlog.AppendResult) error {
		mentions := parseWebMentions(req.Content, members)
		envelope, err := channel.EncodeGroupOutboxEnvelope(mentions)
		if err != nil {
			return fmt.Errorf("encode outbox envelope: %w", err)
		}
		_, err = q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
			ID:             uuid.Must(uuid.NewV7()).String(),
			GroupMessageID: result.Message.ID,
			GroupID:        result.GroupID,
			Envelope:       envelope,
			Status:         "running",
			AttemptCount:   0,
			LeaseUntil:     pgtype.Timestamptz{Time: time.Now().UTC().Add(groupOutboxLeaseDuration), Valid: true},
			NextAttemptAt:  pgtype.Timestamptz{},
			LastError:      "",
		})
		if err != nil {
			return fmt.Errorf("create group outbox: %w", err)
		}
		return nil
	}))
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	if !appendResult.Inserted {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		streamEmptyGroupReply(w, flusher)
		return
	}

	outbox, err := s.q.GetGroupOutboxByMessage(ctx, appendResult.Message.ID)
	if err != nil {
		s.writeInternalError(w, err)
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

	publisher := &webGroupPublisher{w: w, flusher: flusher}
	publisher.writeSSE(map[string]string{"type": "start", "messageId": uuid.Must(uuid.NewV7()).String()})
	// Deliberately based on runtimeCtx, not the drain context: DispatchSync runs
	// the group turn itself, so a drain-start cancellation would kill in-flight
	// work the graceful HTTP shutdown budget exists to finish. runtimeCtx is
	// cancelled only after the HTTP drain completes.
	dispatchCtx, cancelDispatch := context.WithTimeout(s.runtimeCtx, groupOutboxLeaseDuration)
	defer cancelDispatch()
	if err := s.groupDispatcher.DispatchSync(dispatchCtx, outbox, publisher); err != nil {
		publisher.writeSSE(map[string]string{"type": "error", "errorText": err.Error()})
	}
	publisher.writeSSE(map[string]string{"type": "finish"})
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// parseWebMentions extracts @AgentID patterns from message text and returns
// them as Mention structs with AgentID pre-resolved (Web has no platform-level
// bot identity to resolve).
func parseWebMentions(content string, members []sqlc.ChannelGroupMember) []pkgchannel.Mention {
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.AgentID] = struct{}{}
	}
	var mentions []pkgchannel.Mention
	for w := range strings.FieldsSeq(content) {
		after, ok := strings.CutPrefix(w, "@")
		if !ok {
			continue
		}
		if _, isMember := memberSet[after]; isMember {
			mentions = append(mentions, pkgchannel.Mention{Raw: "@" + after, AgentID: after})
		}
	}
	return mentions
}

// handleGroupCommand intercepts slash commands in group chat.
// Returns the reply text and true if handled.
func (s *Server) handleGroupCommand(ctx context.Context, groupID, content string, members []sqlc.ChannelGroupMember) (string, bool) {
	cmd, ok := parseCommand(content)
	if !ok {
		return "", false
	}
	switch cmd {
	case "/config":
		return "⚠️ /config is not available in group chats. Please use it in a direct message.", true
	case "/compact":
		// Group history is assembled from the group event log, not per-agent LCM
		// conversations, so compaction does not apply here (see
		// agent.ErrGroupCompactionUnsupported). Report it plainly instead of
		// running a private-style compaction over an event-log conversation.
		return pkgchannel.GroupCompactUnsupportedMessage, true
	default:
		return "", false
	}
}
