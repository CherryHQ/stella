package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/renderrefs"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
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

const (
	// Allow the aggregate image ceiling after Base64 expansion plus 5 MiB for
	// JSON, text, and field names.
	maxSessionMessageRequestBytes = ai.MaxAggregateImageBytes*4/3 + 5*1024*1024
	// maxSessionMessageParts caps workspace reads per send while leaving room
	// for eight images, their file markers, text, and ordinary attachments.
	maxSessionMessageParts = 32
)

// lifecycleValueContext takes cancellation and deadlines from the server
// lifecycle while preserving request-scoped values for tracing and downstream
// hooks. A browser disconnect therefore detaches transport without orphaning
// the turn from process shutdown.
type lifecycleValueContext struct {
	context.Context
	values context.Context
}

func (c lifecycleValueContext) Value(key any) any {
	if value := c.values.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}

func (s *Server) turnContext(requestCtx context.Context) context.Context {
	lifecycle := s.runtimeCtx
	if lifecycle == nil {
		// Production always injects runtimeCtx. The fallback keeps directly-built
		// handler tests safe without coupling a turn to their request recorder.
		lifecycle = context.Background()
	}
	return lifecycleValueContext{Context: lifecycle, values: context.WithoutCancel(requestCtx)}
}

func (s *Server) SendSessionMessage(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSessionMessageRequestBytes)
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
	// File parts are resolved through the same authorized access the workspace
	// endpoints use, so an attachment reference can only reach files this caller
	// may already read.
	access, err := s.sessionAccess.Begin(r.Context(), authority)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	message, err := partsToMessageContent(r.Context(), workspaceUploadReader(access, agentID, sessionID), body.Parts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message attachments")
		return
	}

	result, err := s.sessionAccess.Send(r.Context(), s.turnContext(r.Context()), sessionaccess.SendInput{Authority: authority, AgentID: agentID, SessionID: sessionID, Message: message})
	if err != nil {
		// An archived session is a state conflict, not a missing one: the client
		// holds a session that was rotated away and needs to move to the new one
		// rather than treat its own link as broken.
		if errors.Is(err, session.ErrArchived) {
			writeError(w, http.StatusConflict, "session is archived; start a new session")
			return
		}
		s.writeSessionAccessError(w, err)
		return
	}
	if result.PlainReply != "" {
		streamPlainReply(w, flusher, result.PlainReply)
		return
	}
	// The response follows the request and drain contexts, but the admitted turn
	// follows the server work lifecycle. Navigation, connection loss, or graceful
	// HTTP drain ends this observer only; accepted-work drain owns the turn.
	sctx, cancel := s.readiness.streamContext(r.Context())
	defer cancel()
	streamAgentEvents(sctx, w, flusher, agentID, sessionID, result.Events, nil)
}

// StopSession explicitly cancels an in-flight turn. Transport disconnects never
// call this endpoint; they only detach an SSE observer.
func (s *Server) StopSession(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	authority, ok := s.sessionAuthority(w, r)
	if !ok {
		return
	}
	if err := s.sessionAccess.Stop(r.Context(), sessionaccess.StopInput{
		Authority: authority,
		AgentID:   agentID,
		SessionID: sessionID,
	}); err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkSessionViewed clears completed-turn attention without altering content.
func (s *Server) MarkSessionViewed(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	authority, ok := s.sessionAuthority(w, r)
	if !ok {
		return
	}
	if err := s.sessionAccess.MarkViewed(r.Context(), sessionaccess.MarkViewedInput{
		Authority: authority,
		AgentID:   agentID,
		SessionID: sessionID,
	}); err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// workspaceRawReader reads an uploaded file's bytes by the path the composer
// referenced. Implementations carry the caller's authority, so the read can only
// reach files that caller may already see.
type workspaceRawReader func(ctx context.Context, path string) ([]byte, error)

// workspaceUploadReader reads composer uploads through the session's authorized
// workspace access. Uploads land in the user scope; an absolute sandbox-view
// path is canonicalized against the roots this caller may already read.
func workspaceUploadReader(access *sessionaccess.Access, agentID, sessionID string) workspaceRawReader {
	return func(ctx context.Context, path string) ([]byte, error) {
		res, err := access.ReadWorkspacePath(ctx, sessionaccess.WorkspaceReadInput{
			AgentID: agentID, SessionID: sessionID,
			Scope: sessionaccess.WorkspaceScopeUser, Path: path, Raw: true,
			MaxBytes: ai.MaxImageInputBytes,
		})
		if err != nil {
			return nil, err
		}
		return res.RawContent, nil
	}
}

// partsToMessageContent converts API MessageParts to internal MessageContent.
// File parts name a workspace upload, so they are resolved through read rather
// than trusted as paths. Image limits are enforced before Base64 allocation and
// before the handler begins its SSE turn.
func partsToMessageContent(ctx context.Context, read workspaceRawReader, parts []apitypes.MessagePart) (agent.MessageContent, error) {
	if len(parts) > maxSessionMessageParts {
		return nil, fmt.Errorf("too many message parts: %d exceeds %d", len(parts), maxSessionMessageParts)
	}
	if len(parts) == 1 && parts[0].Type == apitypes.MessagePartTypeText && parts[0].Text != nil {
		return *parts[0].Text, nil
	}

	budget := sessionImageBudget{}
	blocks := make([]ai.ContentBlock, 0, len(parts)*2)
	for _, p := range parts {
		switch p.Type {
		case apitypes.MessagePartTypeText:
			if p.Text != nil {
				blocks = append(blocks, ai.TextContent{Text: *p.Text})
			}
		case apitypes.MessagePartTypeImage:
			if p.Image == nil {
				continue
			}
			if err := budget.add(conservativeBase64DecodedLen(*p.Image)); err != nil {
				return nil, err
			}
			mime := "image/png"
			if p.MimeType != nil {
				mime = *p.MimeType
			}
			blocks = append(blocks, ai.ImageContent{Data: *p.Image, MimeType: mime})
		case apitypes.MessagePartTypeFile:
			path := strings.TrimSpace(derefStr(p.Url))
			if path == "" {
				continue
			}
			marker := ai.TextContent{Text: fmt.Sprintf("[file: %s]", path)}
			blocks = append(blocks, marker)
			if read == nil {
				continue
			}
			data, err := read(ctx, path)
			if err != nil {
				continue
			}
			imageMime := pkgtools.DetectImageMime(data)
			if imageMime == "" || len(data) > ai.MaxImageInputBytes {
				continue
			}
			if err := budget.add(len(data)); err != nil {
				return nil, err
			}
			blocks = append(blocks, ai.ImageContent{
				Data:     base64.StdEncoding.EncodeToString(data),
				MimeType: imageMime,
			})
		}
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return blocks, nil
}

type sessionImageBudget struct {
	count int
	bytes int
}

func (b *sessionImageBudget) add(rawBytes int) error {
	if rawBytes > ai.MaxImageInputBytes {
		return fmt.Errorf("image input exceeds %d bytes", ai.MaxImageInputBytes)
	}
	b.count++
	if b.count > ai.MaxImagesPerMessage {
		return fmt.Errorf("too many images: %d exceeds %d", b.count, ai.MaxImagesPerMessage)
	}
	b.bytes += rawBytes
	if b.bytes > ai.MaxAggregateImageBytes {
		return fmt.Errorf("image inputs exceed %d bytes", ai.MaxAggregateImageBytes)
	}
	return nil
}

// conservativeBase64DecodedLen gives a no-allocation upper bound even when a
// client supplied malformed or unpadded Base64. Canonical enrichment performs
// the exact decode and validation later.
func conservativeBase64DecodedLen(encoded string) int {
	return ((len(encoded) + 3) / 4) * 3
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
	ID             string    `json:"id"`
	Channel        string    `json:"channel"`
	Kind           string    `json:"kind"`
	ProjectID      string    `json:"project_id,omitempty"`
	Title          string    `json:"title"`
	AgentID        string    `json:"agent_id"`
	UserID         string    `json:"user_id"`
	CreatedAt      time.Time `json:"created_at"`
	LastActive     time.Time `json:"last_active"`
	ActivityStatus string    `json:"activity_status"`
	Archived       bool      `json:"archived"`
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
		ID:             info.ID,
		Channel:        info.Channel,
		Kind:           info.Kind,
		ProjectID:      info.ProjectID,
		Title:          info.Title,
		AgentID:        info.AgentID,
		UserID:         info.UserID,
		CreatedAt:      info.CreatedAt.UTC(),
		LastActive:     info.LastActive.UTC(),
		ActivityStatus: sessionActivityStatus(info),
		Archived:       info.Archived,
	}
}

func sessionActivityStatus(info session.Info) string {
	if !info.LastTurnCompletedAt.IsZero() && info.LastTurnCompletedAt.After(info.LastViewedAt) {
		return "unread"
	}
	return "idle"
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
	page, err := access.ListPage(r.Context(), agentID, opts, limit)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}

	resp := make([]sessionResponse, 0, len(page.Sessions))
	for _, si := range page.Sessions {
		item := sessionResponseFromInfo(si)
		if access.SessionRunning(si) {
			item.ActivityStatus = "running"
		}
		resp = append(resp, item)
	}
	out := map[string]any{"sessions": resp}
	if page.HasMore {
		out["next_page_token"] = encodeOffsetToken(page.NextOffset)
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
	if access.SessionRunning(detail.Info) {
		resp.ActivityStatus = "running"
	}

	// Resolve user name from the account system (best-effort display enrichment).
	if detail.Info.UserID != "" {
		if email, err := s.account.LookupEmail(r.Context(), detail.Info.UserID); err == nil {
			resp.UserName = email
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
	resp := sessionResponseFromInfo(si)
	if access.SessionRunning(si) {
		resp.ActivityStatus = "running"
	}
	writeData(w, http.StatusOK, resp)
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
	writeData(w, http.StatusOK, apitypes.SessionMessageList{Messages: serializeDBMessages(agentID, sessionID, messages)})
}

// GetSessionMedia returns immutable bytes only after the Session PEP proves the
// routed session can read a part that references them.
func (s *Server) GetSessionMedia(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, mediaID string) {
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	media, err := access.ReadMedia(r.Context(), agentID, sessionID, mediaID)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	w.Header().Set("Content-Type", media.MimeType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", media.SHA256))
	http.ServeContent(w, r, media.ID, time.Time{}, bytes.NewReader(media.Data))
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
	if err != nil || int64(offset)+int64(pageSize)+1 > math.MaxInt32 {
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
	if result.Raw {
		w.Header().Set("Content-Type", result.RawMediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": result.RawName}))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// private: session-scoped authorization; no-cache: store but revalidate,
		// so a reloaded transcript turns repeat image bodies into 304s while a
		// rewritten workspace file is still picked up immediately.
		w.Header().Set("Cache-Control", "private, no-cache")
		http.ServeContent(w, r, result.RawName, result.RawModTime, bytes.NewReader(result.RawContent))
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

// serializeDBMessages converts raw DB message rows to the typed history
// contract. Keep assistant-row grouping in sync with ListMessagesByLogicalPage:
// the SQL query uses the same logical-message boundary so pagination never
// splits a rendered message.
func serializeDBMessages(agentID, sessionID string, rows []sessionaccess.Message) []apitypes.SessionMessage {
	result := make([]apitypes.SessionMessage, 0, len(rows))
	for i := 0; i < len(rows); {
		row := rows[i]
		switch row.Role {
		case "user":
			result = append(result, serializeUserRow(agentID, sessionID, row))
			i++
		case "assistant":
			message, consumed := serializeAssistantRows(rows, i)
			result = append(result, message)
			i += consumed
		case "tool":
			result = append(result, serializeToolRow(agentID, sessionID, row))
			i++
		default:
			i++
		}
	}
	return result
}

func serializeUserRow(agentID, sessionID string, row sessionaccess.Message) apitypes.SessionMessage {
	message := apitypes.SessionMessage{
		Id: row.ID, Role: apitypes.SessionMessageRoleUser,
		Timestamp: row.CreatedAt.UTC(), TokenCount: row.TokenCount,
	}
	setSessionMessagePresentation(&message, agentID, sessionID, row.Content, row.Parts)
	return message
}

func serializeAssistantRows(rows []sessionaccess.Message, start int) (apitypes.SessionMessage, int) {
	blocks := make([]apitypes.SessionMessageBlock, 0)
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
			blocks = append(blocks, apitypes.SessionMessageBlock{Type: apitypes.SessionMessageBlockTypeThinking, Thinking: &row.Content})
		case "tool_call":
			blocks = append(blocks, decodeToolCallBlock(row.Content))
		default:
			blocks = append(blocks, apitypes.SessionMessageBlock{Type: apitypes.SessionMessageBlockTypeText, Text: &row.Content})
		}
		consumed++
	}

	return apitypes.SessionMessage{
		// First row's id identifies the merged turn — stable across pagination
		// regardless of how many earlier pages have been loaded.
		Id: rows[start].ID, Role: apitypes.SessionMessageRoleAssistant, Blocks: &blocks,
		Timestamp: rows[start].CreatedAt.UTC(), TokenCount: totalTokens,
	}, consumed
}

func decodeToolCallBlock(content string) apitypes.SessionMessageBlock {
	var env struct {
		ID   string          `json:"id"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		name := "unknown"
		args := map[string]any{}
		return apitypes.SessionMessageBlock{Type: apitypes.SessionMessageBlockTypeToolCall, Name: &name, Arguments: &args}
	}
	var args map[string]any
	_ = json.Unmarshal(env.Args, &args)
	if args == nil {
		args = map[string]any{}
	}
	return apitypes.SessionMessageBlock{Type: apitypes.SessionMessageBlockTypeToolCall, Id: &env.ID, Name: &env.Tool, Arguments: &args}
}

func serializeToolRow(agentID, sessionID string, row sessionaccess.Message) apitypes.SessionMessage {
	message := apitypes.SessionMessage{
		Id: row.ID, Role: apitypes.SessionMessageRoleTool, Timestamp: row.CreatedAt.UTC(), TokenCount: row.TokenCount,
	}
	var env struct {
		ID         string                 `json:"id"`
		Tool       string                 `json:"tool"`
		Result     json.RawMessage        `json:"result"`
		Error      string                 `json:"error,omitempty"`
		IsError    bool                   `json:"is_error"`
		References []renderrefs.Reference `json:"references,omitempty"`
	}
	if err := json.Unmarshal([]byte(row.Content), &env); err != nil {
		// Malformed envelope: best-effort — show raw content, no ID to match.
		name, isError := "", false
		message.ToolName, message.IsError = &name, &isError
		setSessionMessagePresentation(&message, agentID, sessionID, row.Content, row.Parts)
		return message
	}
	// Try to decode result as a plain string first; fall back to raw JSON bytes.
	var text string
	if err := json.Unmarshal(env.Result, &text); err != nil {
		text = string(env.Result)
	}
	isError := env.IsError || env.Error != ""
	message.ToolCallId, message.ToolName, message.IsError = &env.ID, &env.Tool, &isError
	setSessionMessagePresentation(&message, agentID, sessionID, text, row.Parts)
	if len(env.References) > 0 {
		references := make([]apitypes.SessionMessageReference, 0, len(env.References))
		for _, ref := range env.References {
			apiRef := apitypes.SessionMessageReference{V: ref.V, Type: ref.Type, Id: ref.ID}
			if ref.AgentID != "" {
				apiRef.AgentId = &ref.AgentID
			}
			if ref.Intent != "" {
				apiRef.Intent = &ref.Intent
			}
			if ref.Preview != nil {
				preview := struct {
					Status *string `json:"status,omitempty"`
					Title  *string `json:"title,omitempty"`
				}{Status: &ref.Preview.Status, Title: &ref.Preview.Title}
				apiRef.Preview = &preview
			}
			references = append(references, apiRef)
		}
		message.References = &references
	}
	return message
}

// setSessionMessagePresentation uses durable parts as the complete visible
// projection when they exist. Parent message content contains the baseline used
// by model context and must never leak beside the original image in history.
func setSessionMessagePresentation(message *apitypes.SessionMessage, agentID, sessionID, fallback string, parts []sessionaccess.MessagePart) {
	if len(parts) == 0 {
		message.Content = &fallback
		return
	}
	blocks := sessionMessageParts(agentID, sessionID, parts)
	content := sessionMessageText(blocks)
	message.Content = &content
	if len(blocks) > 0 {
		message.Blocks = &blocks
	}
}

func sessionMessageParts(agentID, sessionID string, parts []sessionaccess.MessagePart) []apitypes.SessionMessageBlock {
	blocks := make([]apitypes.SessionMessageBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			blocks = append(blocks, apitypes.SessionMessageBlock{Type: apitypes.SessionMessageBlockTypeText, Text: &part.Text})
		case "image":
			if part.MediaID == "" || part.MimeType == "" {
				continue
			}
			url := sessionMediaURL(agentID, sessionID, part.MediaID)
			blocks = append(blocks, apitypes.SessionMessageBlock{
				Type: apitypes.SessionMessageBlockTypeImage, MediaId: &part.MediaID, MimeType: &part.MimeType, Url: &url,
			})
		}
	}
	return blocks
}

func sessionMessageText(blocks []apitypes.SessionMessageBlock) string {
	text := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == apitypes.SessionMessageBlockTypeText && block.Text != nil {
			text = append(text, *block.Text)
		}
	}
	return strings.Join(text, "\n")
}

func sessionMediaURL(agentID, sessionID, mediaID string) string {
	return fmt.Sprintf("/api/agents/%s/sessions/%s/media/%s", url.PathEscape(agentID), url.PathEscape(sessionID), url.PathEscape(mediaID))
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
