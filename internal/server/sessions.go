package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/sessionaccess"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request, agentID string) {
	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}

	var body apiserver.CreateSessionJSONRequestBody
	if err := decodeJSON(r, &body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request")
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
		info, err = access.ResolveMain(r.Context(), authInfo.UserID, agentID)
	} else {
		info, err = access.Create(r.Context(), authInfo.UserID, agentID, projectID, kind, session.ChannelWeb)
	}
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}

	writeData(w, http.StatusCreated, sessionResponseFromInfo(info))
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	authority, ok := s.sessionAuthority(w, r)
	if !ok {
		return
	}

	result, err := s.sessionAccess.Send(r.Context(), sessionaccess.SendInput{Authority: authority, AgentID: agentID, SessionID: sessionID, Message: partsToMessageContent(body.Parts)})
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	if result.PlainReply != "" {
		streamPlainReply(w, flusher, result.PlainReply)
		return
	}
	// Deliberately NOT drain-cancelled: the turn above runs on the request
	// context, so ending this stream at drain start would kill the in-flight
	// turn — the exact work the graceful HTTP shutdown budget exists to finish.
	// The stream ends when the turn completes; force-close is the backstop.
	streamAgentEvents(r.Context(), w, flusher, agentID, sessionID, result.Events, nil)
}

// streamAgentEvents encodes a live turn's events to w as a Vercel AI-SDK UI
// message stream (SSE). Shared by SendSessionMessage (the turn it initiated) and
// StreamSessionEvents (a read-only subscription to a turn started elsewhere), so
// both emit the exact wire format the web chat parser expects. The stream ends
// when ch closes (turn finished) or ctx is cancelled (client disconnected).
func streamAgentEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, ch <-chan agent.Event, beforeProtectedEvent func() error) {
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

			if evt.Store != nil {
				continue
			}
			// Attach subscriptions must re-authorize at delivery time. A denial
			// terminates the connection before the source event is encoded; Send
			// passes nil because its one initial use-case evaluation covers chunks.
			if beforeProtectedEvent != nil {
				if err := beforeProtectedEvent(); err != nil {
					return
				}
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	authority, ok := s.sessionAuthority(w, r)
	if !ok {
		return
	}

	attach, err := s.sessionAccess.Attach(r.Context(), sessionaccess.AttachInput{Authority: authority, AgentID: agentID, SessionID: sessionID})
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	defer attach.Cancel()

	// No turn in flight: 204 tells the AI-SDK resume client there is nothing to
	// reconnect to, so it stays on the static transcript instead of holding the
	// connection open.
	if !attach.Live {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	sctx, cancel := s.readiness.streamContext(r.Context())
	defer cancel()
	streamAgentEvents(sctx, w, flusher, agentID, sessionID, attach.Events, func() error {
		return attach.BeforeProtectedEvent(r.Context())
	})
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

// sessionResponse is the HTTP representation of session-domain metadata.
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

// sessionResponseFromInfo renders a session-domain Info directly, without
// round-tripping through the memory persistence record just to build a DTO.
func sessionResponseFromInfo(info session.Info) sessionResponse {
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

func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request, agentID string, params apiserver.ListSessionsParams) {
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	opts := session.ListOptions{
		IncludeArchived: true,
		Limit:           limit + 1,
		Offset:          offset,
	}
	if params.Kind == nil {
		opts.ExcludeInternal = true
	} else {
		opts.Kinds = []session.Kind{session.Kind(*params.Kind)}
	}
	if params.ProjectId != nil {
		opts.ProjectID = *params.ProjectId
	}
	sessions, err := access.List(r.Context(), agentID, opts)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}

	sessions, nextToken := nextPageTokenForRows(sessions, limit, offset)
	resp := make([]sessionResponse, 0, len(sessions))
	for _, si := range sessions {
		resp = append(resp, sessionResponseFromInfo(si))
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
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	detail, err := access.Detail(r.Context(), agentID, sessionID)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}

	resp := sessionDetailResponse{
		sessionResponse: sessionResponseFromInfo(detail.Info),
		AgentName:       detail.AgentName,
	}

	// Resolve user name from auth system.
	if detail.Info.UserID != "" && s.users != nil {
		authUser, err := s.users.GetUser(r.Context(), detail.Info.UserID)
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
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	si, err := access.Write(r.Context(), agentID, sessionID)
	if err != nil {
		s.writeSessionAccessError(w, err)
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

	if err := access.UpdateTitle(r.Context(), si, title); err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	si.Title = title
	writeData(w, http.StatusOK, sessionResponseFromInfo(si))
}

func (s *Server) DeleteSession(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	si, err := access.Delete(r.Context(), agentID, sessionID)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	if err := access.Archive(r.Context(), si); err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) GetSessionMessages(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetSessionMessagesParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	limit := 20
	if params.Limit != nil && *params.Limit >= 0 {
		limit = *params.Limit
	}
	skip := 0
	if params.Skip != nil && *params.Skip >= 0 {
		skip = *params.Skip
	}
	messages, err := access.ListMessages(r.Context(), sessionaccess.MessageListInput{
		AgentID: agentID, SessionID: sessionID, Limit: limit, Skip: skip,
		After: params.After, Before: params.Before, SeqFrom: params.SeqFrom, SeqTo: params.SeqTo,
	})
	if err != nil {
		if errors.Is(err, sessionaccess.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "invalid seq range")
			return
		}
		s.writeSessionAccessError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"messages": serializeDBMessages(messages)})
}

func (s *Server) GetSessionContextItems(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetSessionContextItemsParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	access, ok := s.beginSessionAccess(w, r)
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
	page, err := access.ListContextItems(r.Context(), sessionaccess.ContextItemListInput{AgentID: agentID, SessionID: sessionID, PageSize: pageSize, Offset: offset})
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	var nextPageToken *string
	if page.HasNextOffset {
		tok := encodeOffsetToken(page.NextOffset)
		nextPageToken = &tok
	}
	writeData(w, http.StatusOK, apitypes.SessionContextItemList{
		Items:         contextItemsToAPI(page.Items),
		Meta:          contextMetaToAPI(page.Meta),
		NextPageToken: nextPageToken,
	})
}

func (s *Server) GetSessionSummary(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, summaryID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	detail, err := access.GetSummary(r.Context(), agentID, sessionID, summaryID)
	if err != nil {
		if errors.Is(err, sessionaccess.ErrSummaryNotFound) {
			writeError(w, http.StatusNotFound, "summary not found")
			return
		}
		s.writeSessionAccessError(w, err)
		return
	}
	writeData(w, http.StatusOK, summaryDetailToAPI(detail))
}

// beginSessionAccess mints no identity: AuthInfo is verified by middleware and
// is the only trusted HTTP authority source. Every handler calls it once, then
// carries the returned Access through the whole use case.
func (s *Server) sessionAuthority(w http.ResponseWriter, r *http.Request) (authz.Authority, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return authz.Authority{}, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return authz.Authority{}, false
	}
	return authority, true
}

func (s *Server) beginSessionAccess(w http.ResponseWriter, r *http.Request) (*sessionaccess.Access, bool) {
	authority, ok := s.sessionAuthority(w, r)
	if !ok {
		return nil, false
	}
	access, err := s.sessionAccess.Begin(r.Context(), authority)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return nil, false
	}
	return access, true
}

func (s *Server) writeSessionAccessError(w http.ResponseWriter, err error) {
	if errors.Is(err, sessionaccess.ErrUnavailable) {
		s.log.Error("session access", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Existing sessions remain opaque on every authorization denial.
	writeError(w, http.StatusNotFound, "session not found")
}

func (s *Server) GetSessionWorkspace(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetSessionWorkspaceParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	listPath := ""
	if params.Path != nil {
		listPath = *params.Path
	}
	depth := 2
	if params.Depth != nil && *params.Depth > 0 {
		depth = *params.Depth
	}
	info, err := access.ListWorkspace(r.Context(), sessionaccess.WorkspaceListInput{
		AgentID: agentID, SessionID: sessionID, Scope: workspaceScope(params.Scope),
		ShowHidden: params.ShowHidden != nil && *params.ShowHidden, Path: listPath, Depth: depth,
	})
	if err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	writeData(w, http.StatusOK, info)
}

func workspaceScope(scope *apitypes.WorkspaceScope) sessionaccess.WorkspaceScope {
	if scope != nil && *scope == apitypes.WorkspaceScopeUser {
		return sessionaccess.WorkspaceScopeUser
	}
	return sessionaccess.WorkspaceScopeAgent
}

func (s *Server) beginWorkspaceAccess(w http.ResponseWriter, r *http.Request, sessionID string) (*sessionaccess.Access, bool) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return nil, false
	}
	return s.beginSessionAccess(w, r)
}

func (s *Server) writeWorkspaceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sessionaccess.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid request")
	case errors.Is(err, sessionaccess.ErrIsDir):
		writeError(w, http.StatusBadRequest, "path is a directory")
	case errors.Is(err, sessionaccess.ErrBinary):
		writeError(w, http.StatusBadRequest, "file appears to be binary")
	case errors.Is(err, sessionaccess.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, sessionaccess.ErrForbidden), errors.Is(err, sessionaccess.ErrUnavailable):
		s.writeSessionAccessError(w, err)
	default:
		s.writeInternalError(w, err)
	}
}

func (s *Server) CreateWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.CreateWorkspaceFileParams) {
	access, ok := s.beginWorkspaceAccess(w, r, sessionID)
	if !ok {
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
	content := ""
	if body.Content != nil {
		content = *body.Content
	}
	info, err := access.CreateWorkspacePath(r.Context(), sessionaccess.WorkspaceCreateInput{
		AgentID: agentID, SessionID: sessionID, Scope: workspaceScope(params.Scope),
		Path: body.Path, Content: content, IsDir: body.IsDir != nil && *body.IsDir,
	})
	if err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, info)
}

func (s *Server) DeleteWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.DeleteWorkspaceFileParams) {
	access, ok := s.beginWorkspaceAccess(w, r, sessionID)
	if !ok {
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
	if err := access.DeleteWorkspacePath(r.Context(), sessionaccess.WorkspacePathInput{AgentID: agentID, SessionID: sessionID, Scope: workspaceScope(params.Scope), Path: body.Path}); err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) MoveWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.MoveWorkspaceFileParams) {
	access, ok := s.beginWorkspaceAccess(w, r, sessionID)
	if !ok {
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
	info, err := access.MoveWorkspacePath(r.Context(), sessionaccess.WorkspaceMoveInput{AgentID: agentID, SessionID: sessionID, Scope: workspaceScope(params.Scope), Path: body.Path, NewPath: body.NewPath})
	if err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	writeData(w, http.StatusOK, info)
}

func (s *Server) GetWorkspaceFileContent(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.GetWorkspaceFileContentParams) {
	access, ok := s.beginWorkspaceAccess(w, r, sessionID)
	if !ok {
		return
	}
	if params.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	result, err := access.ReadWorkspacePath(r.Context(), sessionaccess.WorkspaceReadInput{AgentID: agentID, SessionID: sessionID, Scope: workspaceScope(params.Scope), Path: params.Path, Raw: params.Raw != nil && *params.Raw})
	if err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	if result.RawFilePath != "" {
		w.Header().Del("Content-Type")
		w.Header().Set("Content-Disposition", "inline")
		http.ServeFile(w, r, result.RawFilePath)
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) UpdateWorkspaceFileContent(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.UpdateWorkspaceFileContentParams) {
	access, ok := s.beginWorkspaceAccess(w, r, sessionID)
	if !ok {
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
	result, err := access.WriteWorkspacePath(r.Context(), sessionaccess.WorkspaceWriteInput{AgentID: agentID, SessionID: sessionID, Scope: workspaceScope(params.Scope), Path: body.Path, Content: body.Content})
	if err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) UploadWorkspaceFile(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	access, ok := s.beginWorkspaceAccess(w, r, sessionID)
	if !ok {
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
	result, err := access.UploadWorkspacePath(r.Context(), sessionaccess.WorkspaceUploadInput{AgentID: agentID, SessionID: sessionID, Filename: header.Filename, Reader: file, Now: time.Now()})
	if err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, result)
}

func (s *Server) GetSessionSystemPrompt(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	authority, ok := s.sessionAuthority(w, r)
	if !ok {
		return
	}
	systemPrompt, err := s.sessionAccess.GetSystemPrompt(r.Context(), sessionaccess.SystemPromptInput{Authority: authority, AgentID: agentID, SessionID: sessionID})
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"system_prompt": systemPrompt})
}

func contextItemsToAPI(items []sessionaccess.ContextItem) []apitypes.SessionContextItem {
	out := make([]apitypes.SessionContextItem, 0, len(items))
	for _, item := range items {
		apiItem := apitypes.SessionContextItem{Ordinal: item.Ordinal, EventType: item.EventType}
		switch {
		case item.Message != nil:
			apiItem.Type = apitypes.Message
			apiItem.Message = &apitypes.SessionContextMessage{
				Id: item.Message.ID, Seq: item.Message.Seq, Role: item.Message.Role,
				EventType: item.Message.EventType, Content: item.Message.Content,
				Timestamp: item.Message.Timestamp.UTC(), TokenCount: item.Message.TokenCount,
			}
		case item.Summary != nil:
			apiItem.Type = apitypes.Summary
			summary := summaryToAPI(*item.Summary)
			apiItem.Summary = &summary
		default:
			continue
		}
		out = append(out, apiItem)
	}
	return out
}

func contextMetaToAPI(meta sessionaccess.ContextMeta) apitypes.SessionContextMeta {
	return apitypes.SessionContextMeta{MessageCount: meta.MessageCount, SourceTokenCount: meta.SourceTokenCount, ActiveTokenCount: meta.ActiveTokenCount, SummaryDepth: meta.SummaryDepth}
}

func summaryDetailToAPI(detail sessionaccess.SummaryDetail) apitypes.SessionSummaryDetail {
	resp := apitypes.SessionSummaryDetail{Summary: summaryToAPI(detail.Summary), Children: make([]apitypes.SessionContextSummary, 0, len(detail.Children))}
	for _, child := range detail.Children {
		resp.Children = append(resp.Children, summaryToAPI(child))
	}
	if detail.MessageSeqFrom > 0 && detail.MessageSeqTo > 0 {
		resp.MessageSeqFrom = &detail.MessageSeqFrom
		resp.MessageSeqTo = &detail.MessageSeqTo
	}
	return resp
}

func summaryToAPI(s sessionaccess.Summary) apitypes.SessionContextSummary {
	return apitypes.SessionContextSummary{
		Id: s.ID, Kind: s.Kind, Depth: s.Depth, Content: s.Content, TokenCount: s.TokenCount,
		EarliestAt: s.EarliestAt, LatestAt: s.LatestAt, DescendantCount: s.DescendantCount,
		DescendantTokenCount: s.DescendantTokenCount, SourceMessageTokenCount: s.SourceMessageTokenCount,
		CreatedAt: s.CreatedAt.UTC(),
	}
}

// serializeDBMessages converts raw DB message rows to JSON-friendly maps,
// preserving the created_at timestamp from the database. Keep assistant-row
// grouping in sync with ListMessagesByLogicalPage: the SQL query uses the same
// logical-message boundary so paginated responses never split a rendered message.
func serializeDBMessages(rows []sessionaccess.Message) []map[string]any {
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

func serializeUserRow(row sessionaccess.Message) map[string]any {
	return map[string]any{
		"id":          row.ID,
		"role":        "user",
		"timestamp":   row.CreatedAt.UTC(),
		"content":     row.Content,
		"token_count": row.TokenCount,
	}
}

func serializeAssistantRows(rows []sessionaccess.Message, start int) (map[string]any, int) {
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

func serializeToolRow(row sessionaccess.Message) map[string]any {
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
