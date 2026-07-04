package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/pluginhost"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request, agentID string) {
	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}

	var body apiserver.CreateSessionJSONRequestBody
	if err := decodeJSON(r, &body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	svc := s.poolManager.GetService(agentID)
	if svc == nil {
		writeError(w, http.StatusBadRequest, "no service available for the given agent_id")
		return
	}

	kind := session.KindChat
	if body.Kind != nil {
		kind = session.Kind(*body.Kind)
		if kind != session.KindChat && kind != session.KindMain {
			writeError(w, http.StatusBadRequest, "unsupported session kind")
			return
		}
	}
	projectID := ""
	if body.ProjectId != nil {
		projectID = *body.ProjectId
	}

	var info session.Info
	var err error
	if kind == session.KindMain && projectID == "" {
		info, err = svc.ResolveMainSession(r.Context(), authInfo.UserID, agentID)
	} else {
		info, err = svc.NewSession(r.Context(), authInfo.UserID, agentID, projectID, kind, session.ChannelWeb)
	}
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeData(w, http.StatusCreated, toSessionResponse(info))
}

func (s *Server) SendSessionMessage(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	var body apiserver.SendSessionMessageJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(body.Parts) == 0 {
		writeError(w, http.StatusBadRequest, "parts is required")
		return
	}

	// Authorize through the single chokepoint: ownership, agent/path match, and
	// the scoped-token session pin. A scoped (e.g. sandbox) token must not reach
	// a session other than the one it is pinned to.
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}

	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	ctx := memoryContext(r, agentID)
	si, err := sm.LoadInfo(ctx, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	var svc *agent.Service
	if si.AgentID != "" {
		svc = s.poolManager.GetService(si.AgentID)
	} else {
		svc = s.poolManager.Default()
	}
	if svc == nil {
		writeError(w, http.StatusBadRequest, "no service available for this session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Convert AI SDK parts to internal MessageContent.
	msgContent := partsToMessageContent(body.Parts)

	// Humans may join any session kind (chat/main and internal delegate/task/
	// scheduler sessions alike). The runtime's chat loop is kind-agnostic, so a
	// manual message simply resumes the agent on that session's context.
	siKind := session.Kind(si.Kind)

	// Intercept slash commands before hitting the LLM.
	if text, ok := msgContent.(string); ok {
		if reply, handled := handleSessionCommand(ctx, svc, si, text); handled {
			streamPlainReply(w, flusher, reply)
			return
		}
	}

	ch := svc.Chat(ctx, agent.ChatRequest{
		SessionID: sessionID,
		UserID:    si.UserID,
		AgentID:   si.AgentID,
		Kind:      siKind,
		Channel:   session.Channel(si.Channel),
		Message:   msgContent,
	})
	streamAgentEvents(r.Context(), w, flusher, agentID, sessionID, ch)
}

// streamAgentEvents encodes a live turn's events to w as a Vercel AI-SDK UI
// message stream (SSE). Shared by SendSessionMessage (the turn it initiated) and
// StreamSessionEvents (a read-only subscription to a turn started elsewhere), so
// both emit the exact wire format the web chat parser expects. The stream ends
// when ch closes (turn finished) or ctx is cancelled (client disconnected).
func streamAgentEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, ch <-chan agent.Event) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeData := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	writeDone := func() {
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	messageID := uuid.Must(uuid.NewV7()).String()
	writeData(map[string]string{"type": "start", "messageId": messageID})

	var (
		inText      bool
		textID      string
		inReasoning bool
		reasoningID string
		stepOpen    bool
	)

	closeText := func() {
		if inText {
			writeData(map[string]string{"type": "text-end", "id": textID})
			inText = false
		}
	}
	closeReasoning := func() {
		if inReasoning {
			writeData(map[string]string{"type": "reasoning-end", "id": reasoningID})
			inReasoning = false
		}
	}

	for {
		select {
		case <-ctx.Done():
			closeText()
			closeReasoning()
			if stepOpen {
				writeData(map[string]string{"type": "finish-step"})
			}
			writeData(map[string]string{"type": "finish"})
			writeDone()
			return
		case evt, open := <-ch:
			if !open {
				closeText()
				closeReasoning()
				if stepOpen {
					writeData(map[string]string{"type": "finish-step"})
				}
				writeData(map[string]string{"type": "finish"})
				writeDone()
				return
			}

			if evt.Err != nil {
				closeText()
				closeReasoning()
				writeData(map[string]string{"type": "error", "errorText": evt.Err.Error()})
				if stepOpen {
					writeData(map[string]string{"type": "finish-step"})
				}
				writeData(map[string]string{"type": "finish"})
				writeDone()
				return
			}

			if evt.Store != nil {
				continue
			}

			if evt.Step != nil {
				switch evt.Step.Kind {
				case "start":
					closeText()
					closeReasoning()
					if stepOpen {
						writeData(map[string]string{"type": "finish-step"})
					}
					writeData(map[string]string{"type": "start-step"})
					stepOpen = true
				case "finish":
					closeText()
					closeReasoning()
					if stepOpen {
						writeData(map[string]string{"type": "finish-step"})
						stepOpen = false
					}
				}
				continue
			}

			if evt.Reasoning != "" {
				closeText()
				if !inReasoning {
					reasoningID = uuid.Must(uuid.NewV7()).String()
					writeData(map[string]string{"type": "reasoning-start", "id": reasoningID})
					inReasoning = true
				}
				writeData(map[string]any{"type": "reasoning-delta", "id": reasoningID, "delta": evt.Reasoning})
				continue
			}

			if evt.Text != "" {
				closeReasoning()
				if !inText {
					textID = uuid.Must(uuid.NewV7()).String()
					writeData(map[string]string{"type": "text-start", "id": textID})
					inText = true
				}
				writeData(map[string]any{"type": "text-delta", "id": textID, "delta": evt.Text})
				continue
			}

			if evt.ToolUse != nil {
				closeText()
				closeReasoning()
				tu := evt.ToolUse
				switch tu.Status {
				case "running":
					writeData(map[string]any{
						"type":       "tool-input-start",
						"toolCallId": tu.ID,
						"toolName":   tu.Tool,
						"dynamic":    true,
					})
					args := tu.Arguments
					if args == nil {
						args = map[string]any{"input": tu.Input}
					}
					writeData(map[string]any{
						"type":       "tool-input-available",
						"toolCallId": tu.ID,
						"toolName":   tu.Tool,
						"dynamic":    true,
						"input":      args,
					})
				case "done":
					if len(tu.References) > 0 {
						writeData(map[string]any{
							"type": "data-tool-references",
							"id":   tu.ID,
							"data": map[string]any{"toolCallId": tu.ID, "references": tu.References},
						})
					}
					writeData(map[string]any{
						"type":       "tool-output-available",
						"toolCallId": tu.ID,
						"output":     tu.Content,
					})
				case "error":
					writeData(map[string]any{
						"type":       "tool-output-error",
						"toolCallId": tu.ID,
						"errorText":  tu.Content,
					})
				}
				continue
			}

			if evt.Image != nil {
				closeText()
				closeReasoning()
				dataURI := "data:" + evt.Image.MimeType + ";base64," + evt.Image.Data
				writeData(map[string]string{
					"type":      "file",
					"url":       dataURI,
					"mediaType": evt.Image.MimeType,
				})
				continue
			}

			if evt.File != nil {
				closeText()
				closeReasoning()
				fileURL := fmt.Sprintf("/api/agents/%s/sessions/%s/workspace/file-content?path=%s&raw=true",
					agentID, sessionID, evt.File.Path)
				mediaType := detectMIME(evt.File.Name)
				writeData(map[string]string{
					"type":      "file",
					"url":       fileURL,
					"mediaType": mediaType,
				})
				continue
			}
		}
	}
}

// StreamSessionEvents subscribes to a session's in-flight turn and streams its
// events read-only, regardless of who initiated the turn. This lets the web UI
// watch server-driven turns (scheduler/task/delegate) or a turn started in
// another tab in real time, since those turns carry no HTTP request of their own.
func (s *Server) StreamSessionEvents(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	// Same authorization chokepoint as message-send: ownership, agent/path
	// match, and scoped-token session pin. The live event stream is at least as
	// sensitive as the transcript, so it must not be reachable cross-session.
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}

	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	ctx := memoryContext(r, agentID)
	si, err := sm.LoadInfo(ctx, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	var svc *agent.Service
	if si.AgentID != "" {
		svc = s.poolManager.GetService(si.AgentID)
	} else {
		svc = s.poolManager.Default()
	}
	if svc == nil {
		writeError(w, http.StatusBadRequest, "no service available for this session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch, cancel := svc.SubscribeSession(sessionID)
	defer cancel()

	// No turn in flight: 204 tells the AI-SDK resume client there is nothing to
	// reconnect to, so it stays on the static transcript instead of holding the
	// connection open.
	if !svc.SessionLive(sessionID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	streamAgentEvents(r.Context(), w, flusher, agentID, sessionID, ch)
}

// partsToMessageContent converts API MessageParts to internal MessageContent.
func partsToMessageContent(parts []apitypes.MessagePart) agent.MessageContent {
	if len(parts) == 1 && parts[0].Type == apitypes.Text && parts[0].Text != nil {
		return *parts[0].Text
	}
	var blocks []ai.ContentBlock
	for _, p := range parts {
		switch p.Type {
		case apitypes.Text:
			if p.Text != nil {
				blocks = append(blocks, ai.TextContent{Text: *p.Text})
			}
		case apitypes.Image:
			if p.Image != nil {
				mime := "image/png"
				if p.MimeType != nil {
					mime = *p.MimeType
				}
				blocks = append(blocks, ai.ImageContent{Data: *p.Image, MimeType: mime})
			}
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return blocks
}

// detectMIME returns a MIME type based on file extension.
func detectMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	default:
		return "application/octet-stream"
	}
}

// sessionResponse is a JSON-friendly representation of memory.SessionInfo.
type sessionResponse struct {
	ID         string    `json:"id"`
	Channel    string    `json:"channel"`
	Kind       string    `json:"kind"`
	ProjectID  string    `json:"project_id,omitempty"`
	Title      string    `json:"title"`
	AgentID    string    `json:"agent_id"`
	UserID     string    `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
	Archived   bool      `json:"archived"`
}

// sessionDetailResponse extends sessionResponse with resolved names.
type sessionDetailResponse struct {
	sessionResponse
	AgentName string `json:"agent_name"`
	UserName  string `json:"user_name"`
}

func toSessionResponse(info memory.SessionInfo) sessionResponse {
	return sessionResponse{
		ID:         info.ID,
		Channel:    info.Channel,
		Kind:       info.Kind,
		ProjectID:  info.ProjectID,
		Title:      info.Title,
		AgentID:    info.AgentID,
		UserID:     info.UserID,
		CreatedAt:  info.CreatedAt.UTC(),
		LastActive: info.LastActive.UTC(),
		Archived:   info.Archived,
	}
}

func memoryUserContext(r *http.Request) context.Context {
	ctx := r.Context()
	if info := UserFromContext(ctx); info != nil && info.UserID != "" {
		ctx = authz.WithUserID(ctx, info.UserID)
	}
	return ctx
}

func memoryContext(r *http.Request, agentID string) context.Context {
	ctx := memoryUserContext(r)
	if agentID != "" {
		ctx = authz.WithAgentID(ctx, agentID)
	}
	return ctx
}

func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListSessionsParams) {
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeData(w, http.StatusOK, map[string]any{"sessions": []any{}})
		return
	}
	info := UserFromContext(r.Context())

	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	opts := memory.ListOptions{
		IncludeArchived: true,
		ExcludeInternal: params.Kind == nil,
		Limit:           limit + 1,
		Offset:          offset,
	}
	if info != nil {
		opts.UserID = info.UserID
	}
	if params.Kind != nil {
		opts.Kind = string(*params.Kind)
	}
	opts.AgentID = agentID
	if params.ProjectId != nil {
		opts.ProjectID = *params.ProjectId
	}
	sessions, err := sm.ListInfo(memoryContext(r, agentID), opts)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	sessions, nextToken := nextPageTokenForRows(sessions, limit, offset)
	resp := make([]sessionResponse, 0, len(sessions))
	for _, si := range sessions {
		resp = append(resp, toSessionResponse(si))
	}
	out := map[string]any{"sessions": resp}
	if nextToken != "" {
		out["next_page_token"] = nextToken
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}
	si, err := sm.LoadInfo(memoryContext(r, agentID), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	resp := sessionDetailResponse{
		sessionResponse: toSessionResponse(si),
	}
	info := si

	// Resolve agent name.
	if info.AgentID != "" {
		agent, err := s.store.GetAgent(r.Context(), info.AgentID)
		if err == nil {
			resp.AgentName = agent.Name
		}
	}

	// Resolve user name from auth system.
	if info.UserID != "" && s.users != nil {
		authUser, err := s.users.GetUser(r.Context(), info.UserID)
		if err == nil {
			resp.UserName = authUser.Email
		}
	}

	writeData(w, http.StatusOK, resp)
}

func (s *Server) UpdateSession(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}

	var body apiserver.UpdateSessionJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Title == nil {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	title := strings.TrimSpace(*body.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	info := UserFromContext(r.Context())
	if info == nil || info.UserID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if err := s.q.UpdateConversationTitleBySessionID(r.Context(), sqlc.UpdateConversationTitleBySessionIDParams{
		Title:     pgtype.Text{String: title, Valid: true},
		SessionID: sessionID,
		UserID:    pgtype.Text{String: info.UserID, Valid: true},
		AgentID:   pgtype.Text{String: agentID, Valid: agentID != ""},
	}); err != nil {
		s.writeInternalError(w, err)
		return
	}

	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}
	si, err := sm.LoadInfo(memoryContext(r, agentID), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeData(w, http.StatusOK, toSessionResponse(si))
}

func (s *Server) DeleteSession(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}
	info := UserFromContext(r.Context())
	if info == nil || info.UserID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if err := s.q.UpdateConversationArchived(r.Context(), sqlc.UpdateConversationArchivedParams{
		Archived:  true,
		SessionID: sessionID,
		UserID:    pgtype.Text{String: info.UserID, Valid: true},
		AgentID:   pgtype.Text{String: agentID, Valid: agentID != ""},
	}); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) GetSessionMessages(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetSessionMessagesParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	// Ownership check for non-admin users.
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}

	limit := 20
	if params.Limit != nil && *params.Limit >= 0 {
		limit = *params.Limit
	}
	// skip counts serialized messages from the end, enabling backwards pagination.
	// skip=0 → last 20 messages; skip=20 → messages 20–40 from the end, etc.
	skip := 0
	if params.Skip != nil && *params.Skip >= 0 {
		skip = *params.Skip
	}

	// Load raw DB rows to preserve created_at timestamps.
	userID := ""
	if info := UserFromContext(r.Context()); info != nil {
		userID = info.UserID
	}
	conv, err := s.q.GetConversationBySessionID(memoryContext(r, agentID), sqlc.GetConversationBySessionIDParams{SessionID: sessionID, UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: agentID != ""}})
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var rows []sqlc.CtxMessage
	switch {
	case params.SeqFrom != nil || params.SeqTo != nil:
		if params.SeqFrom == nil || params.SeqTo == nil || *params.SeqFrom <= 0 || *params.SeqTo < *params.SeqFrom {
			writeError(w, http.StatusBadRequest, "invalid seq range")
			return
		}
		rows, err = s.q.GetMessagesByConversationRange(r.Context(), sqlc.GetMessagesByConversationRangeParams{
			ConversationID: conv.ID,
			Seq:            int64(*params.SeqFrom),
			Seq_2:          int64(*params.SeqTo),
		})
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
	case limit > 0:
		pageRows, err := s.q.ListMessagesByLogicalPage(r.Context(), sqlc.ListMessagesByLogicalPageParams{
			ConversationID: conv.ID,
			After:          nullTimeFromStringPtr(params.After),
			Before:         nullTimeFromStringPtr(params.Before),
			Limit:          int32(limit),
			Offset:         int32(skip),
		})
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		rows = logicalPageRowsToMessages(pageRows)
	default:
		var err error
		rows, err = s.q.GetMessagesByConversation(r.Context(), conv.ID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		rows = filterMessageRowsByTime(rows, params.After, params.Before)
	}

	writeData(w, http.StatusOK, map[string]any{"messages": serializeDBMessages(rows)})
}

func (s *Server) GetSessionContextItems(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetSessionContextItemsParams) {
	conv, ok := s.requireSessionConversation(w, r, agentID, sessionID)
	if !ok {
		return
	}
	pageSize := 100
	if params.PageSize != nil && *params.PageSize > 0 {
		pageSize = *params.PageSize
	}
	if pageSize > 500 {
		pageSize = 500
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}
	rows, err := s.q.ListContextItemsPage(r.Context(), sqlc.ListContextItemsPageParams{
		ConversationID: conv.ID,
		LimitCount:     int32(pageSize + 1),
		OffsetCount:    int32(offset),
	})
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	usingMessageFallback := false
	if len(rows) == 0 {
		// Distinguish "conversation predates ctx_item" from an out-of-range
		// page token: only fall back to raw messages when no items exist.
		count, err := s.q.GetContextItemCount(r.Context(), conv.ID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		if count == 0 {
			messages, err := s.q.GetMessagesByConversation(r.Context(), conv.ID)
			if err != nil {
				s.writeInternalError(w, err)
				return
			}
			rows = contextRowsFromMessages(messages)
			if offset < len(rows) {
				rows = rows[offset:]
			} else {
				rows = nil
			}
			usingMessageFallback = true
		}
	}
	stats, err := s.q.GetContextStats(r.Context(), conv.ID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	var nextPageToken *string
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		tok := encodeOffsetToken(offset + pageSize)
		nextPageToken = &tok
	}
	items := make([]apitypes.SessionContextItem, 0, len(rows))
	for _, row := range rows {
		if item, ok := contextItemFromRow(row); ok {
			items = append(items, item)
		}
	}
	activeTokenCount := int(stats.ActiveTokenCount)
	if usingMessageFallback {
		activeTokenCount = int(stats.SourceTokenCount)
	}
	writeData(w, http.StatusOK, apitypes.SessionContextItemList{
		Items: items,
		Meta: apitypes.SessionContextMeta{
			MessageCount:     int(stats.MessageCount),
			SourceTokenCount: int(stats.SourceTokenCount),
			ActiveTokenCount: activeTokenCount,
			SummaryDepth:     int(stats.SummaryDepth),
		},
		NextPageToken: nextPageToken,
	})
}

func (s *Server) GetSessionSummary(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, summaryID string) {
	conv, ok := s.requireSessionConversation(w, r, agentID, sessionID)
	if !ok {
		return
	}
	summary, err := s.q.GetSummary(r.Context(), sqlc.GetSummaryParams{ID: summaryID, ConversationID: conv.ID})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "summary not found")
		} else {
			s.writeInternalError(w, err)
		}
		return
	}
	// ctx_summary_parent rows are written as (summary_id=condensed,
	// parent_summary_id=constituent), so "parents" is the downward direction:
	// GetSummaryParents returns the sub-summaries a condensed node was built from.
	children, err := s.q.GetSummaryParents(r.Context(), summaryID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	from, to, err := s.summaryMessageSeqRange(r.Context(), summaryID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	resp := apitypes.SessionSummaryDetail{
		Summary:  summaryToAPI(summary),
		Children: make([]apitypes.SessionContextSummary, 0, len(children)),
	}
	for _, child := range children {
		resp.Children = append(resp.Children, summaryToAPI(child))
	}
	if from > 0 && to > 0 {
		resp.MessageSeqFrom = &from
		resp.MessageSeqTo = &to
	}
	writeData(w, http.StatusOK, resp)
}

// summaryMessageSeqRange aggregates the covered message seq range across the
// summary hierarchy: only leaf summaries carry ctx_summary_message links, so
// condensed nodes walk down through their constituents.
func (s *Server) summaryMessageSeqRange(ctx context.Context, summaryID string) (int, int, error) {
	from, to := 0, 0
	queue := []string{summaryID}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := s.q.GetSummaryMessageSeqRange(ctx, id)
		if err != nil {
			return 0, 0, err
		}
		if r.MessageSeqFrom > 0 {
			if from == 0 || int(r.MessageSeqFrom) < from {
				from = int(r.MessageSeqFrom)
			}
			if int(r.MessageSeqTo) > to {
				to = int(r.MessageSeqTo)
			}
		}
		kids, err := s.q.GetSummaryParents(ctx, id)
		if err != nil {
			return 0, 0, err
		}
		for _, k := range kids {
			queue = append(queue, k.ID)
		}
	}
	return from, to, nil
}

func (s *Server) requireSessionConversation(w http.ResponseWriter, r *http.Request, agentID, sessionID string) (sqlc.CtxConversation, bool) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return sqlc.CtxConversation{}, false
	}
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return sqlc.CtxConversation{}, false
	}
	userID := ""
	if info := UserFromContext(r.Context()); info != nil {
		userID = info.UserID
	}
	conv, err := s.q.GetConversationBySessionID(memoryContext(r, agentID), sqlc.GetConversationBySessionIDParams{SessionID: sessionID, UserID: pgtype.Text{String: userID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: agentID != ""}})
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return sqlc.CtxConversation{}, false
	}
	return conv, true
}

// checkSessionAccess verifies the current user has access to the session.
// Returns a non-nil error (and writes the HTTP response) if access is denied.
func (s *Server) checkSessionAccess(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) error {
	info := UserFromContext(r.Context())
	sm, ok := s.mem.(memory.SessionManager)
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return fmt.Errorf("authentication required")
	}
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return fmt.Errorf("unsupported")
	}
	si, err := sm.LoadInfo(memoryContext(r, agentID), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return err
	}
	if si.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return fmt.Errorf("access denied")
	}
	if agentID != "" && si.AgentID != agentID {
		writeError(w, http.StatusNotFound, "session not found")
		return fmt.Errorf("session not found")
	}
	if _, boundSession, ok := info.scopedBoundary(); ok && boundSession != "" && sessionID != boundSession {
		writeError(w, http.StatusForbidden, "permission denied")
		return fmt.Errorf("permission denied")
	}
	return nil
}

func (s *Server) GetSessionWorkspace(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetSessionWorkspaceParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}
	info, err := sm.LoadInfo(memoryContext(r, agentID), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if info.UserID == "" || info.AgentID == "" {
		writeData(w, http.StatusOK, workspaceDiskInfo{Root: "", Paths: []string{}})
		return
	}
	if _, err := agent.SetupUserWorkspace(config.StellaHome(), info.UserID, info.AgentID); err != nil {
		s.writeInternalError(w, err)
		return
	}
	// Root at whichever scope the caller asked for: the agent's private home
	// (sandbox /workspace, default) or the shared user-data root (sandbox /user).
	root := workspaceRootForScope(info.UserID, info.AgentID, params.Scope)
	showHidden := params.ShowHidden != nil && *params.ShowHidden
	listPath := ""
	if params.Path != nil {
		listPath = *params.Path
	}
	depth := 2
	if params.Depth != nil && *params.Depth > 0 {
		depth = *params.Depth
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, showHidden, listPath, depth)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	// SandboxRoot is the path the agent actually sees for this dir (/workspace or
	// /user on isolating backends); Root stays the host path the file API resolves
	// against.
	diskInfo.SandboxRoot = scopeSandboxView(s.sandboxBackend(r.Context()), root, params.Scope)
	writeData(w, http.StatusOK, diskInfo)
}

// workspaceRootForScope returns the on-disk root for the requested scope under a
// principal's home: the per-agent private home (sandbox /workspace) for "agent"
// (the default), or the shared user-data root (sandbox /user) for "user". Each
// scope is a single root, so the safePath invariant (one root + relative path)
// holds for whichever scope a file op targets.
func workspaceRootForScope(userID, agentID string, scope *apitypes.WorkspaceScope) string {
	if scope != nil && *scope == apitypes.WorkspaceScopeUser {
		return agent.UserDataDir(agent.UserHomeDir(config.StellaHome(), userID))
	}
	return agent.UserAgentDir(config.StellaHome(), userID, agentID)
}

// sandboxBackend returns the name of the active sandbox backend, or "" if the
// plugin list can't be read (treated as a non-isolating backend by callers).
func (s *Server) sandboxBackend(ctx context.Context) string {
	plugins, _ := s.store.ListPlugins(ctx)
	return config.ActiveSandboxBackend(plugins)
}

// scopeSandboxView maps a scope's host root to the path the agent sees for it
// inside the sandbox (/workspace for agent scope, /user for user scope).
func scopeSandboxView(backend, root string, scope *apitypes.WorkspaceScope) string {
	if scope != nil && *scope == apitypes.WorkspaceScopeUser {
		return sandbox.UserDataViewFor(backend, root)
	}
	return sandbox.WorkspaceViewFor(backend, root)
}

type workspaceDiskInfo struct {
	Root        string   `json:"root"`
	SandboxRoot string   `json:"sandbox_root"`
	Paths       []string `json:"paths"`
	TotalFiles  int      `json:"total_files"`
	TotalDirs   int      `json:"total_dirs"`
	TotalBytes  int64    `json:"total_bytes"`
}

func pathDepth(path string) int {
	return len(strings.Split(filepath.ToSlash(path), "/"))
}

func collectWorkspaceDiskInfo(root string, showHidden bool, listPath string, depth int) (workspaceDiskInfo, error) {
	info := workspaceDiskInfo{Root: root, Paths: []string{}}
	listRoot, err := safePath(root, strings.TrimSuffix(listPath, "/"))
	if err != nil {
		return workspaceDiskInfo{}, err
	}
	if stat, statErr := os.Stat(listRoot); statErr != nil {
		return workspaceDiskInfo{}, statErr
	} else if !stat.IsDir() {
		return workspaceDiskInfo{}, fmt.Errorf("path is not a directory")
	}
	err = filepath.WalkDir(listRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		scopeRel, scopeRelErr := filepath.Rel(listRoot, path)
		if scopeRelErr != nil || scopeRel == "." {
			return nil //nolint:nilerr
		}
		if depth > 0 && pathDepth(scopeRel) > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil //nolint:nilerr
		}
		name := d.Name()
		isDot := strings.HasPrefix(name, ".") || strings.Contains(rel, string(filepath.Separator)+".")
		if isDot && !showHidden {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			info.TotalDirs++
			info.Paths = append(info.Paths, filepath.ToSlash(rel)+"/")
			return nil
		}
		entryInfo, statErr := d.Info()
		if statErr != nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		info.TotalFiles++
		if entryInfo.Mode().IsRegular() {
			info.TotalBytes += entryInfo.Size()
		}
		info.Paths = append(info.Paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return workspaceDiskInfo{}, err
	}
	return info, nil
}

// sessionWorkspaceRoot resolves the workspace root for a session, checking
// access and returning (root, nil) or writing an error and returning ("", err).
func (s *Server) sessionWorkspaceRoot(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, scope *apitypes.WorkspaceScope) (string, error) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return "", fmt.Errorf("missing session ID")
	}
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return "", err
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return "", fmt.Errorf("unsupported")
	}
	info, err := sm.LoadInfo(memoryContext(r, agentID), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return "", err
	}
	if info.UserID == "" || info.AgentID == "" {
		writeError(w, http.StatusNotFound, "session has no workspace")
		return "", fmt.Errorf("no workspace")
	}
	if _, err := agent.SetupUserWorkspace(config.StellaHome(), info.UserID, info.AgentID); err != nil {
		s.writeInternalError(w, err)
		return "", err
	}
	// Root at the requested scope, matching GetSessionWorkspace — file ops stay
	// within that single root and never reach the other.
	return workspaceRootForScope(info.UserID, info.AgentID, scope), nil
}

// safePath resolves a caller-supplied relative path to an absolute path that
// is guaranteed to stay within root. Returns an error if the result would
// escape root (directory traversal).
func safePath(root, rel string) (string, error) {
	return sharepkg.SafePath(root, rel)
}

func (s *Server) CreateWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.CreateWorkspaceFileParams) {
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID, params.Scope)
	if err != nil {
		return
	}
	var body apiserver.CreateWorkspaceFileJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.IsDir != nil && *body.IsDir {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			s.writeInternalError(w, err)
			return
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			s.writeInternalError(w, err)
			return
		}
		content := ""
		if body.Content != nil {
			content = *body.Content
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			s.writeInternalError(w, err)
			return
		}
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, false, "", 0)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, diskInfo)
}

func (s *Server) DeleteWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.DeleteWorkspaceFileParams) {
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID, params.Scope)
	if err != nil {
		return
	}
	var body apiserver.DeleteWorkspaceFileJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) MoveWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.MoveWorkspaceFileParams) {
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID, params.Scope)
	if err != nil {
		return
	}
	var body apiserver.MoveWorkspaceFileJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Path == "" || body.NewPath == "" {
		writeError(w, http.StatusBadRequest, "path and new_path are required")
		return
	}
	src, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	dst, err := safePath(root, body.NewPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		s.writeInternalError(w, err)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		s.writeInternalError(w, err)
		return
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, false, "", 0)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, diskInfo)
}

func (s *Server) GetWorkspaceFileContent(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetWorkspaceFileContentParams) {
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID, params.Scope)
	if err != nil {
		return
	}
	if params.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, params.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if params.Raw != nil && *params.Raw {
		// jsonMiddleware pre-sets application/json for every /api/ route; clear it
		// so ServeFile can detect the real type. Otherwise top-level navigations
		// (which don't content-sniff) render image bytes as garbled text.
		w.Header().Del("Content-Type")
		w.Header().Set("Content-Disposition", "inline")
		http.ServeFile(w, r, abs)
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	// Refuse binary files: check first 512 bytes for null byte.
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if slices.Contains(probe, 0) {
		writeError(w, http.StatusBadRequest, "file appears to be binary")
		return
	}
	lang := detectLanguage(params.Path)
	writeData(w, http.StatusOK, map[string]any{
		"path":     params.Path,
		"content":  string(data),
		"language": lang,
	})
}

func (s *Server) UpdateWorkspaceFileContent(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.UpdateWorkspaceFileContentParams) {
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID, params.Scope)
	if err != nil {
		return
	}
	var body apiserver.UpdateWorkspaceFileContentJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.writeInternalError(w, err)
		return
	}
	if err := os.WriteFile(abs, []byte(body.Content), 0o644); err != nil {
		s.writeInternalError(w, err)
		return
	}
	lang := detectLanguage(body.Path)
	writeData(w, http.StatusOK, map[string]any{
		"path":     body.Path,
		"content":  body.Content,
		"language": lang,
	})
}

func (s *Server) UploadWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	// Uploads are user-shared, not agent-private: always root at the shared
	// user-data root (sandbox /user) so the file lands beside channel uploads and
	// every one of the user's agents can reach it.
	userScope := apitypes.WorkspaceScopeUser
	root, err := s.sessionWorkspaceRoot(w, r, agentID, sessionID, &userScope)
	if err != nil {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	now := time.Now()
	hash := fmt.Sprintf("%06x", now.UnixNano()&0xFFFFFF)
	dir := filepath.Join(root, "assets", now.Format("200601"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.writeInternalError(w, err)
		return
	}
	name := fmt.Sprintf("%s-%s-%s", now.Format("20060102"), hash, filepath.Base(header.Filename))
	abs := filepath.Join(dir, name)
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		s.writeInternalError(w, err)
		return
	}
	// Return the sandbox-visible path (e.g. /user/assets/...) so the agent can
	// open the upload directly from its message text.
	rel, _ := filepath.Rel(root, abs)
	sandboxRoot := sandbox.UserDataViewFor(s.sandboxBackend(r.Context()), root)
	writeData(w, http.StatusCreated, map[string]string{"path": filepath.ToSlash(filepath.Join(sandboxRoot, rel))})
}

// detectLanguage returns a simple language hint based on file extension.
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md", ".mdx":
		return "markdown"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".sh", ".bash":
		return "shell"
	case ".sql":
		return "sql"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".txt":
		return "text"
	default:
		return ""
	}
}

func (s *Server) GetSessionSystemPrompt(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	// Ownership check for non-admin users.
	if err := s.checkSessionAccess(w, r, agentID, sessionID); err != nil {
		return
	}

	info, err := sm.LoadInfo(memoryContext(r, agentID), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Look up agent config.
	var agentCfg config.Agent
	if info.AgentID != "" {
		agentCfg, _ = s.store.GetAgent(r.Context(), info.AgentID)
	}
	var userRoot string
	if info.UserID != "" && info.AgentID != "" {
		if userDir, err := agent.SetupUserWorkspace(config.StellaHome(), info.UserID, info.AgentID); err == nil {
			userRoot = userDir
		}
	}
	projectRoot, err := s.projectRootForSession(memoryContext(r, agentID), agentID, &sessionID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	homeDir, _ := os.UserHomeDir()
	pluginView, err := s.pluginHost.SessionPluginView(r.Context())
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	promptBuild := pkgplugins.SystemPromptContext{
		StellaHome:          config.StellaHome(),
		HomeDir:             homeDir,
		AgentRoot:           agentCfg.Workspace,
		ProjectRoot:         projectRoot,
		UserID:              info.UserID,
		AgentID:             info.AgentID,
		UserRoot:            userRoot,
		WorkspaceRoot:       userRoot,
		SkillStore:          pluginhost.NewSkillStoreAdapter(s.skillStore()),
		RegisteredPluginIDs: pluginView.RegisteredPluginIDs,
		EnabledPluginIDs:    pluginView.EnabledPluginIDs,
	}
	promptSections, err := s.pluginHost.SystemPromptSections(r.Context(), promptBuild)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	if skillsSection, err := skillstool.BuildPromptSection(r.Context(), promptBuild); err != nil {
		s.writeInternalError(w, err)
		return
	} else if skillsSection.Title != "" && skillsSection.Content != "" {
		promptSections = append(promptSections, skillsSection)
	}
	systemPrompt := prompt.BuildSystemPromptFromDB(r.Context(), prompt.DBPromptParams{
		SystemPrompt: agentCfg.SystemPrompt,
		Memory:       s.mem,
		UserID:       info.UserID,
		AgentID:      info.AgentID,
		StellaHome:   config.StellaHome(),
		AgentRoot:    agentCfg.Workspace,
		UserRoot:     userRoot,
		Sections:     append(promptSections, s.pluginHost.ManifestPluginPrompts()...),
	})

	writeData(w, http.StatusOK, map[string]string{"system_prompt": systemPrompt})
}

func nullTimeFromStringPtr(p *string) pgtype.Timestamptz {
	if p == nil {
		return pgtype.Timestamptz{}
	}
	// An unparseable after/before yields the zero time; treat that as "no bound"
	// (NULL) rather than epoch-zero. Passing 0001-01-01 to the page query would
	// silently empty the page (created_at <= zero) or no-op the filter
	// (created_at >= zero) instead of ignoring the malformed bound.
	t := parseTime(*p)
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func logicalPageRowsToMessages(rows []sqlc.ListMessagesByLogicalPageRow) []sqlc.CtxMessage {
	out := make([]sqlc.CtxMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, sqlc.CtxMessage(row))
	}
	return out
}

func filterMessageRowsByTime(rows []sqlc.CtxMessage, after, before *string) []sqlc.CtxMessage {
	if after == nil && before == nil {
		return rows
	}
	var afterT, beforeT time.Time
	if after != nil {
		afterT = parseTime(*after)
	}
	if before != nil {
		beforeT = parseTime(*before)
	}
	filtered := rows[:0]
	for _, row := range rows {
		// A zero bound means the param was absent or unparseable; skip it rather
		// than comparing against epoch-zero (which would drop every row for a bad
		// `before`). Matches the DB-path guard in nullTimeFromStringPtr.
		if !afterT.IsZero() && row.CreatedAt.Before(afterT) {
			continue
		}
		if !beforeT.IsZero() && row.CreatedAt.After(beforeT) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func contextRowsFromMessages(messages []sqlc.CtxMessage) []sqlc.ListContextItemsPageRow {
	rows := make([]sqlc.ListContextItemsPageRow, 0, len(messages))
	for i, msg := range messages {
		rows = append(rows, sqlc.ListContextItemsPageRow{
			Ordinal:           int64(i + 1),
			ItemType:          "message",
			EventType:         msg.EventType,
			MessageID:         pgtype.Text{String: msg.ID, Valid: true},
			MessageSeq:        pgtype.Int8{Int64: msg.Seq, Valid: true},
			MessageRole:       pgtype.Text{String: msg.Role, Valid: true},
			MessageEventType:  pgtype.Text{String: msg.EventType, Valid: true},
			MessageContent:    pgtype.Text{String: msg.Content, Valid: true},
			MessageTokenCount: pgtype.Int8{Int64: msg.TokenCount, Valid: true},
			MessageCreatedAt:  pgtype.Timestamptz{Time: msg.CreatedAt, Valid: true},
		})
	}
	return rows
}

func contextItemFromRow(row sqlc.ListContextItemsPageRow) (apitypes.SessionContextItem, bool) {
	item := apitypes.SessionContextItem{
		Ordinal:   int(row.Ordinal),
		EventType: nullableStringPtr(pgtype.Text{String: row.EventType, Valid: row.EventType != ""}),
	}
	switch row.ItemType {
	case "message":
		if !row.MessageID.Valid || !row.MessageSeq.Valid || !row.MessageRole.Valid {
			return item, false
		}
		content := nullableStringPtr(row.MessageContent)
		eventType := nullableStringPtr(row.MessageEventType)
		item.Type = apitypes.Message
		item.Message = &apitypes.SessionContextMessage{
			Id:         row.MessageID.String,
			Seq:        int(row.MessageSeq.Int64),
			Role:       row.MessageRole.String,
			EventType:  eventType,
			Content:    content,
			Timestamp:  row.MessageCreatedAt.Time.UTC(),
			TokenCount: int(row.MessageTokenCount.Int64),
		}
		return item, true
	case "summary":
		if !row.SummaryID.Valid {
			return item, false
		}
		item.Type = apitypes.Summary
		item.Summary = &apitypes.SessionContextSummary{
			Id:                      row.SummaryID.String,
			Kind:                    row.SummaryKind.String,
			Depth:                   int(row.SummaryDepth.Int64),
			Content:                 row.SummaryContent.String,
			TokenCount:              int(row.SummaryTokenCount.Int64),
			EarliestAt:              parseTimePtr(row.SummaryEarliestAt),
			LatestAt:                parseTimePtr(row.SummaryLatestAt),
			DescendantCount:         int(row.SummaryDescendantCount.Int64),
			DescendantTokenCount:    int(row.SummaryDescendantTokenCount.Int64),
			SourceMessageTokenCount: int(row.SummarySourceMessageTokenCount.Int64),
			CreatedAt:               row.SummaryCreatedAt.Time.UTC(),
		}
		return item, true
	default:
		return item, false
	}
}

func summaryToAPI(s sqlc.CtxSummary) apitypes.SessionContextSummary {
	return apitypes.SessionContextSummary{
		Id:                      s.ID,
		Kind:                    s.Kind,
		Depth:                   int(s.Depth),
		Content:                 s.Content,
		TokenCount:              int(s.TokenCount),
		EarliestAt:              parseTimePtr(s.EarliestAt),
		LatestAt:                parseTimePtr(s.LatestAt),
		DescendantCount:         int(s.DescendantCount),
		DescendantTokenCount:    int(s.DescendantTokenCount),
		SourceMessageTokenCount: int(s.SourceMessageTokenCount),
		CreatedAt:               s.CreatedAt.UTC(),
	}
}

// serializeDBMessages converts raw DB message rows to JSON-friendly maps,
// preserving the created_at timestamp from the database. Keep assistant-row
// grouping in sync with ListMessagesByLogicalPage: the SQL query uses the same
// logical-message boundary so paginated responses never split a rendered message.
func serializeDBMessages(rows []sqlc.CtxMessage) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	i := 0
	for i < len(rows) {
		row := rows[i]
		switch row.Role {
		case "user":
			result = append(result, serializeUserRow(row))
			i++
		case "assistant":
			m, consumed := serializeAssistantRows(rows, i)
			result = append(result, m)
			i += consumed
		case "tool":
			result = append(result, serializeToolRow(row))
			i++
		default:
			i++
		}
	}
	return result
}

func serializeUserRow(row sqlc.CtxMessage) map[string]any {
	return map[string]any{
		"id":          row.ID,
		"role":        "user",
		"timestamp":   row.CreatedAt.UTC(),
		"content":     row.Content,
		"token_count": row.TokenCount,
	}
}

func serializeAssistantRows(rows []sqlc.CtxMessage, start int) (map[string]any, int) {
	var blocks []map[string]any
	var totalTokens int64
	consumed := 0

	// Merge ALL consecutive assistant rows into one turn — text and tool_calls alike.
	// A non-assistant row (user, tool) always breaks the run.
	for start+consumed < len(rows) {
		row := rows[start+consumed]
		if row.Role != "assistant" {
			break
		}
		totalTokens += row.TokenCount
		switch row.EventType {
		case "thinking":
			blocks = append(blocks, map[string]any{"type": "thinking", "thinking": row.Content})
		case "tool_call":
			blocks = append(blocks, decodeToolCallBlock(row.Content))
		default:
			blocks = append(blocks, map[string]any{"type": "text", "text": row.Content})
		}
		consumed++
	}

	return map[string]any{
		// First row's id identifies the merged turn — stable across pagination
		// regardless of how many earlier pages have been loaded.
		"id":          rows[start].ID,
		"role":        "assistant",
		"blocks":      blocks,
		"timestamp":   rows[start].CreatedAt.UTC(),
		"token_count": totalTokens,
	}, consumed
}

func decodeToolCallBlock(content string) map[string]any {
	var env struct {
		ID   string          `json:"id"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return map[string]any{"type": "tool_call", "name": "unknown", "arguments": map[string]any{}}
	}
	var args map[string]any
	_ = json.Unmarshal(env.Args, &args)
	return map[string]any{"type": "tool_call", "id": env.ID, "name": env.Tool, "arguments": args}
}

func serializeToolRow(row sqlc.CtxMessage) map[string]any {
	m := map[string]any{
		"id":          row.ID,
		"role":        "tool",
		"timestamp":   row.CreatedAt.UTC(),
		"token_count": row.TokenCount,
	}
	var env struct {
		ID         string                 `json:"id"`
		Tool       string                 `json:"tool"`
		Result     json.RawMessage        `json:"result"`
		Error      string                 `json:"error,omitempty"`
		References []renderrefs.Reference `json:"references,omitempty"`
	}
	if err := json.Unmarshal([]byte(row.Content), &env); err != nil {
		// Malformed envelope: best-effort — show raw content, no ID to match.
		m["content"] = row.Content
		m["tool_name"] = ""
		m["is_error"] = false
		return m
	}
	// Try to decode result as a plain string first; fall back to raw JSON bytes.
	var text string
	if err := json.Unmarshal(env.Result, &text); err != nil {
		text = string(env.Result)
	}
	m["tool_call_id"] = env.ID
	m["tool_name"] = env.Tool
	m["content"] = text
	m["is_error"] = env.Error != ""
	if len(env.References) > 0 {
		m["references"] = env.References
	}
	return m
}

// parseCommand extracts the slash command from a message, returning the
// lowercase command (e.g. "/compact") and true, or ("", false) if the
// message is not a command.
func parseCommand(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])
	if !strings.HasPrefix(cmd, "/") {
		return "", false
	}
	return cmd, true
}

// handleSessionCommand intercepts slash commands from the web UI so they
// don't get sent to the LLM. Returns the reply text and true if handled.
func handleSessionCommand(ctx context.Context, svc *agent.Service, si memory.SessionInfo, text string) (string, bool) {
	cmd, ok := parseCommand(text)
	if !ok {
		return "", false
	}
	switch cmd {
	case "/compact":
		summary, err := svc.CompactSession(ctx, si)
		if err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return summary, true
	default:
		return "", false
	}
}

// streamPlainReply writes a complete SSE stream for a simple text reply
// (used by slash commands that bypass the LLM).
func streamPlainReply(w http.ResponseWriter, flusher http.Flusher, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	write := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	messageID := uuid.Must(uuid.NewV7()).String()
	textID := uuid.Must(uuid.NewV7()).String()
	write(map[string]string{"type": "start", "messageId": messageID})
	write(map[string]string{"type": "start-step"})
	write(map[string]string{"type": "text-start", "id": textID})
	write(map[string]any{"type": "text-delta", "id": textID, "delta": text})
	write(map[string]string{"type": "text-end", "id": textID})
	write(map[string]string{"type": "finish-step"})
	write(map[string]string{"type": "finish"})
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}
