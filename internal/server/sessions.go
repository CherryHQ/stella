package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/sessionevent"
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
	// Multipart framing gets 1 MiB beyond the 32 MiB file ceiling enforced by
	// session access. MaxBytesReader prevents ParseMultipartForm from spilling an
	// unbounded request to disk.
	maxWorkspaceUploadRequestBytes = 33 << 20
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

	excludedTools := []string(nil)
	if body.ExcludedTools != nil {
		excludedTools = *body.ExcludedTools
	}
	if s.durableRuns != nil && s.sessionEvents != nil {
		s.sendSessionMessageDurable(w, r, flusher, authority, agentID, sessionID, message, excludedTools)
		return
	}
	result, err := s.sessionAccess.Send(r.Context(), s.turnContext(r.Context()), sessionaccess.SendInput{
		Authority:     authority,
		AgentID:       agentID,
		SessionID:     sessionID,
		Message:       message,
		ExcludedTools: excludedTools,
	})
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
	streamAgentEvents(sctx, w, flusher, agentID, sessionID, result.Events, "", 0, nil)
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
	if s.durableRuns != nil {
		// The turn may execute on another replica: flag the durable execution
		// lease and drop still-queued runs so a remote worker aborts/never
		// starts. Local StopSession above covers the single-process case.
		if err := s.cancelDurableTurn(r.Context(), sessionID); err != nil {
			slog.WarnContext(r.Context(), "durable turn cancel failed", "session", sessionID, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelDurableTurn flags the session's live execution lease (a running
// worker aborts at its next lease check) and cancels queued runs so a worker
// elsewhere never starts them.
func (s *Server) cancelDurableTurn(ctx context.Context, sessionID string) error {
	return s.durableRuns.CancelSessionTurn(ctx, sessionID)
}

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

// turnStreamState is the single state machine a turn's SSE encoding runs on:
// which text/reasoning part is open (and under which id) and whether a step
// frame is open. Resume replays the turn's durable events through advance
// with output suppressed, so a reconnecting client re-attaches under the same
// part ids — derived from each part's first durable seq — instead of a second
// part opening mid-turn.
type turnStreamState struct {
	inText      bool
	textID      string
	inReasoning bool
	reasoningID string
	stepOpen    bool
	partID      func(kind string, seq int64) string
}

// advance applies one event to the stream state — the same transitions the
// emit path uses, so priming a cursor and live encoding can never disagree
// about which part is open.
func (s *turnStreamState) advance(evt agent.Event) {
	switch {
	case evt.Err != nil:
		s.inText, s.inReasoning, s.stepOpen = false, false, false
	case evt.Step != nil:
		s.inText, s.inReasoning = false, false
		s.stepOpen = evt.Step.Kind == "start"
	case evt.Reasoning != "":
		s.inText = false
		if !s.inReasoning {
			s.inReasoning = true
			s.reasoningID = s.partID("r", evt.Seq)
		}
	case evt.Text != "":
		s.inReasoning = false
		if !s.inText {
			s.inText = true
			s.textID = s.partID("t", evt.Seq)
		}
	case evt.ToolUse != nil || evt.Image != nil || evt.File != nil || len(evt.References) > 0:
		s.inText, s.inReasoning = false, false
	}
}

// streamAgentEvents encodes a turn's events to w as a Vercel AI-SDK UI
// message stream (SSE). Shared by SendSessionMessage (the turn it initiated)
// and StreamSessionEvents (a read-only tail of a turn started elsewhere), so
// both emit the exact wire format the web chat parser expects. The stream
// ends when ch closes (turn finished) or ctx is cancelled (client
// disconnected). scope, when non-empty, pins message identity to the durable
// turn being tailed and derives part ids from each part's first durable seq;
// scope is empty for non-durable paths, which keep random per-stream ids.
// resumeSeq > 0 suppresses output for events at or before it — their state
// transitions still run, so a resume continues mid-part without re-sending
// consumed text.
func streamAgentEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, ch <-chan agent.Event, scope string, resumeSeq int64, beforeProtectedEvent func() error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// pendingCursor is emitted as an `id:` line only after the durable event's
	// frames are fully flushed — a disconnect mid-event must not advance the
	// cursor past data the client never received.
	var pendingCursor string
	writeData := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	emitCursor := func() {
		if pendingCursor != "" {
			_, _ = fmt.Fprintf(w, "id: %s\n\n", pendingCursor)
			flusher.Flush()
			pendingCursor = ""
		}
	}
	writeDone := func() {
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	messageID := uuid.Must(uuid.NewV7()).String()
	if scope != "" {
		messageID = "msg-" + scope
	}
	writeData(map[string]string{"type": "start", "messageId": messageID})

	st := turnStreamState{
		partID: func(kind string, seq int64) string {
			if scope != "" && seq > 0 {
				return scope + ":" + kind + ":" + strconv.FormatInt(seq, 10)
			}
			return uuid.Must(uuid.NewV7()).String()
		},
	}

	// resumed tracks whether this connection has emitted its part/step open
	// frames. With resumeSeq > 0 the primed state can hold an open part whose
	// start frame the client already consumed — the first unsuppressed event
	// re-opens those frames under the same ids so deltas always follow a start
	// on this connection. Consumed text itself is never re-sent.
	resumed := resumeSeq <= 0
	emitOpen := func() {
		if resumed {
			return
		}
		resumed = true
		if st.stepOpen {
			writeData(map[string]string{"type": "start-step"})
		}
		if st.inReasoning {
			writeData(map[string]string{"type": "reasoning-start", "id": st.reasoningID})
		}
		if st.inText {
			writeData(map[string]string{"type": "text-start", "id": st.textID})
		}
	}

	// emitTransition writes the part/step boundary frames between before and
	// st — ends first (reasoning before text, matching the historical order),
	// then step frames, then opens.
	emitTransition := func(before turnStreamState) {
		if before.inReasoning && !st.inReasoning {
			writeData(map[string]string{"type": "reasoning-end", "id": before.reasoningID})
		}
		if before.inText && !st.inText {
			writeData(map[string]string{"type": "text-end", "id": before.textID})
		}
		if before.stepOpen && !st.stepOpen {
			writeData(map[string]string{"type": "finish-step"})
		}
		if st.stepOpen && !before.stepOpen {
			writeData(map[string]string{"type": "start-step"})
		}
		if st.inReasoning && !before.inReasoning {
			writeData(map[string]string{"type": "reasoning-start", "id": st.reasoningID})
		}
		if st.inText && !before.inText {
			writeData(map[string]string{"type": "text-start", "id": st.textID})
		}
	}
	finish := func() {
		// Only close what this connection opened — a resume that consumed
		// every event never emitted a start, so it must not write bare ends.
		if resumed {
			before := st
			st.inText, st.inReasoning, st.stepOpen = false, false, false
			emitTransition(before)
		}
		writeData(map[string]string{"type": "finish"})
		writeDone()
	}

	for {
		select {
		case <-ctx.Done():
			finish()
			return
		case evt, open := <-ch:
			if !open {
				finish()
				return
			}
			// Resume fast-forward: events at or before the cursor advance the
			// state machine only — the client already consumed their frames.
			if resumeSeq > 0 && evt.Seq > 0 && evt.Seq <= resumeSeq {
				st.advance(evt)
				continue
			}
			pendingCursor = evt.DurableID

			// Attach subscriptions must re-authorize at delivery time. A denial
			// terminates the connection before the source event is encoded; Send
			// passes nil because its one initial use-case evaluation covers chunks.
			if beforeProtectedEvent != nil {
				if err := beforeProtectedEvent(); err != nil {
					return
				}
			}
			// The durable terminal receipt travels as a transient data part —
			// the SDK delivers it to onData without adding it to message parts.
			// It carries the observed session+scope so a late frame from an old
			// observe cannot release a newer turn's pin.
			if evt.Terminal != nil {
				writeData(map[string]any{
					"type":      "data-turn-terminal",
					"transient": true,
					"data": map[string]string{
						"session_id": sessionID,
						"scope":      scope,
						"result":     evt.Terminal.Result,
						"reason":     evt.Terminal.Reason,
					},
				})
				emitCursor()
				continue
			}
			// A combined Store+ToolUse is one atomic loop event. Pure persistence is
			// transport-internal, but its paired tool progress must reach SSE.
			if evt.Store != nil && evt.ToolUse == nil {
				emitCursor()
				continue
			}

			emitOpen()
			before := st
			st.advance(evt)

			switch {
			case evt.Err != nil:
				emitTransition(before)
				writeData(map[string]string{"type": "error", "errorText": evt.Err.Error()})
				writeData(map[string]string{"type": "finish"})
				writeDone()
				emitCursor()
				return

			case evt.Step != nil:
				emitTransition(before)
				emitCursor()

			case evt.Reasoning != "":
				emitTransition(before)
				writeData(map[string]any{"type": "reasoning-delta", "id": st.reasoningID, "delta": evt.Reasoning})
				emitCursor()

			case evt.Text != "":
				emitTransition(before)
				writeData(map[string]any{"type": "text-delta", "id": st.textID, "delta": evt.Text})
				emitCursor()

			case evt.ToolUse != nil:
				emitTransition(before)
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
				emitCursor()

			case evt.Image != nil:
				emitTransition(before)
				dataURI := "data:" + evt.Image.MimeType + ";base64," + evt.Image.Data
				writeData(map[string]string{
					"type":      "file",
					"url":       dataURI,
					"mediaType": evt.Image.MimeType,
				})
				emitCursor()

			case evt.File != nil:
				emitTransition(before)
				fileURL := fmt.Sprintf("/api/agents/%s/sessions/%s/workspace/file-content?path=%s&raw=true",
					agentID, sessionID, evt.File.Path)
				mediaType := detectMIME(evt.File.Name)
				writeData(map[string]string{
					"type":      "file",
					"url":       fileURL,
					"mediaType": mediaType,
				})
				emitCursor()
			}
		}
	}
}

// StreamSessionEvents subscribes to a session's in-flight turn and streams its
// events read-only, regardless of who initiated the turn. This lets the web UI
// watch server-driven turns (scheduler/task/delegate) or a turn started in
// another tab in real time, since those turns carry no HTTP request of their own.
func (s *Server) StreamSessionEvents(w http.ResponseWriter, r *http.Request, agentID string, sessionID string, params apiserver.StreamSessionEventsParams) {
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

	// The durable event log is the only source an observer may tail — a turn
	// may execute on any replica, and the local hub carries wakes, never data.
	// 204 tells the AI-SDK resume client there is nothing to reconnect to, so
	// it stays on the static transcript instead of holding the connection open.
	if s.sessionEvents == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A reconnecting watcher supplies Last-Event-ID — the durable cursor is
	// "scope:seq" (run id or "x"+execution token); anything else parses as
	// seq-less and is honored positionally only.
	var cursor string
	if params.LastEventID != nil {
		cursor = *params.LastEventID
	}
	switch ok, truncated := s.streamDurableTurn(r.Context(), w, flusher, agentID, sessionID, attach, cursor); {
	case ok:
		return
	case truncated:
		// The cursor fell behind the log's retained window: 409 makes the
		// client rebuild from the transcript instead of silently skipping.
		writeError(w, http.StatusConflict, "event cursor expired; reload session")
		return
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
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
	if info.LastTurnCompletedAt.IsZero() || !info.LastTurnCompletedAt.After(info.LastViewedAt) {
		return "idle"
	}
	switch string(info.LastTurnResult) {
	case "success":
		return "success"
	case "error":
		return "error"
	default:
		return "idle"
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
			item.ActivityStatus = "working"
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
		resp.ActivityStatus = "working"
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
		if errors.Is(err, sessionaccess.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "title is too long")
			return
		}
		s.writeSessionAccessError(w, err)
		return
	}
	si.Title = title
	resp := sessionResponseFromInfo(si)
	if access.SessionRunning(si) {
		resp.ActivityStatus = "working"
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
	var snapshotSeq *int64
	if params.SnapshotSeq != nil {
		v := int64(*params.SnapshotSeq)
		snapshotSeq = &v
	}
	messages, err := access.ListMessages(r.Context(), sessionaccess.MessageListInput{
		AgentID: agentID, SessionID: sessionID, Limit: limit, Skip: skip,
		After: params.After, Before: params.Before, SeqFrom: params.SeqFrom, SeqTo: params.SeqTo,
		SnapshotSeq: snapshotSeq,
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

func (s *Server) GetSessionUsage(w http.ResponseWriter, r *http.Request, agentID string, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	access, ok := s.beginSessionAccess(w, r)
	if !ok {
		return
	}
	usage, err := access.Usage(r.Context(), agentID, sessionID)
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	models := make([]apitypes.SessionModelUsage, len(usage.Models))
	for i, item := range usage.Models {
		models[i] = apitypes.SessionModelUsage{
			Provider: item.Provider, Model: item.Model, CallCount: item.CallCount,
			ReportedCallCount: item.ReportedCallCount, PricedCallCount: item.PricedCallCount,
			InputTokens: item.InputTokens, OutputTokens: item.OutputTokens,
			CacheReadTokens: item.CacheReadTokens, CacheWriteTokens: item.CacheWriteTokens,
			CostUsd: item.CostUSD,
		}
	}
	writeData(w, http.StatusOK, apitypes.SessionUsage{
		PendingCallCount: usage.PendingCallCount, CallCount: usage.CallCount, ReportedCallCount: usage.ReportedCallCount, PricedCallCount: usage.PricedCallCount,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens,
		CostUsd: usage.CostUSD, Models: models,
	})
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
	case home.IsOutcomeUnknown(err):
		writeError(w, http.StatusConflict, "workspace mutation outcome is unknown; inspect state before retrying")
	case errors.Is(err, sessionaccess.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "workspace payload exceeds limit")
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
	if err := access.AdmitWorkspaceUpload(r.Context(), agentID, sessionID); err != nil {
		s.writeWorkspaceError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUploadRequestBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "workspace payload exceeds limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer func() { _ = file.Close() }()
	result, err := access.UploadWorkspacePath(r.Context(), sessionaccess.WorkspaceUploadInput{AgentID: agentID, SessionID: sessionID, Filename: header.Filename, Reader: file, ContentLength: &header.Size, Now: time.Now()})
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
				ActorType: apitypes.SessionContextMessageActorType(item.Message.ActorType),
				ActorId:   textPointer(item.Message.ActorID), SourceSessionId: textPointer(item.Message.SourceSessionID),
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
		ActorType: apitypes.SessionMessageActorType(row.ActorType),
		Timestamp: row.CreatedAt.UTC(), TokenCount: row.TokenCount,
	}
	setSessionMessageActor(&message, row)
	setSessionMessagePresentation(&message, agentID, sessionID, row.Content, row.Parts)
	if execution := decodeExecutionMetadata(row.ExecutionMetadata); execution != nil {
		message.Execution = serializeExecutionSummary(execution)
	}
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
		ActorType: apitypes.SessionMessageActorType(rows[start].ActorType),
		ActorId:   textPointer(rows[start].ActorID), SourceSessionId: textPointer(rows[start].SourceSessionID),
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
		ActorType: apitypes.SessionMessageActorType(row.ActorType),
	}
	setSessionMessageActor(&message, row)
	var env struct {
		ID         string                  `json:"id"`
		Tool       string                  `json:"tool"`
		Result     json.RawMessage         `json:"result"`
		Error      string                  `json:"error,omitempty"`
		IsError    bool                    `json:"is_error"`
		ErrorKind  string                  `json:"error_kind,omitempty"`
		References []renderrefs.Reference  `json:"references,omitempty"`
		ChildCalls []ai.ChildToolCallAudit `json:"child_calls,omitempty"`
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
	// Only forwarded when the row recorded it. Rows written before the split
	// carry no kind, and inventing one here would turn "unknown" into a claim.
	if isError && env.ErrorKind != "" {
		kind := apitypes.SessionMessageErrorKind(env.ErrorKind)
		message.ErrorKind = &kind
	}
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
	if len(env.ChildCalls) > 0 {
		childCalls := make([]apitypes.SessionChildToolCallAudit, 0, len(env.ChildCalls))
		for _, child := range env.ChildCalls {
			item := apitypes.SessionChildToolCallAudit{Id: child.ID, Name: child.Name, IsError: child.IsError}
			if child.ErrorKind != "" {
				kind := apitypes.SessionChildToolCallAuditErrorKind(child.ErrorKind)
				item.ErrorKind = &kind
			}
			childCalls = append(childCalls, item)
		}
		message.ChildCalls = &childCalls
	}
	return message
}

func serializeExecutionSummary(summary *ai.ExecutionSummary) *apitypes.SessionExecutionSummary {
	if summary == nil || (len(summary.Plugins) == 0 && len(summary.Skills) == 0) {
		return nil
	}
	plugins := make([]apitypes.SessionExecutionPlugin, 0, len(summary.Plugins))
	for _, plugin := range summary.Plugins {
		item := apitypes.SessionExecutionPlugin{
			PluginId:      plugin.PluginID,
			Authorization: apitypes.SessionExecutionPluginAuthorization(plugin.Authorization),
			Readiness:     apitypes.SessionExecutionPluginReadiness(plugin.Readiness),
		}
		item.PackageVersion = textPointer(plugin.PackageVersion)
		item.PackageDigest = textPointer(plugin.PackageDigest)
		item.Source = textPointer(plugin.Source)
		item.ConfigId = textPointer(plugin.ConfigID)
		item.ConfigScope = textPointer(plugin.ConfigScope)
		if plugin.ConfigRevision != 0 {
			revision := plugin.ConfigRevision
			item.ConfigRevision = &revision
		}
		if len(plugin.Failures) > 0 {
			item.Failures = &plugin.Failures
		}
		if len(plugin.Binaries) > 0 {
			binaries := make([]apitypes.SessionExecutionBinary, 0, len(plugin.Binaries))
			for _, binary := range plugin.Binaries {
				binaries = append(binaries, apitypes.SessionExecutionBinary{
					Name: binary.Name, Tool: textPointer(binary.Tool),
					RequestedVersion: textPointer(binary.RequestedVersion), ResolvedVersion: textPointer(binary.ResolvedVersion),
					Backend: textPointer(binary.Backend), SelectionIdentity: textPointer(binary.SelectionIdentity),
					Source: textPointer(binary.Source),
				})
			}
			item.Binaries = &binaries
		}
		plugins = append(plugins, item)
	}
	result := &apitypes.SessionExecutionSummary{Plugins: plugins}
	if len(summary.Skills) > 0 {
		skills := make([]apitypes.SessionExecutionSkill, 0, len(summary.Skills))
		for _, skill := range summary.Skills {
			item := apitypes.SessionExecutionSkill{
				Name: skill.Name, PluginId: textPointer(skill.PluginID), Version: textPointer(skill.Version),
				Source: textPointer(skill.Source), Scope: textPointer(skill.Scope), Digest: textPointer(skill.Digest),
			}
			if skill.State != "" {
				state := apitypes.SessionExecutionSkillState(skill.State)
				item.State = &state
			}
			skills = append(skills, item)
		}
		result.Skills = &skills
	}
	return result
}

func decodeExecutionMetadata(data []byte) *ai.ExecutionSummary {
	if len(data) == 0 {
		return nil
	}
	var summary ai.ExecutionSummary
	if err := json.Unmarshal(data, &summary); err != nil || (len(summary.Plugins) == 0 && len(summary.Skills) == 0) {
		return nil
	}
	return &summary
}

func setSessionMessageActor(message *apitypes.SessionMessage, row sessionaccess.Message) {
	message.ActorId = textPointer(row.ActorID)
	message.SourceSessionId = textPointer(row.SourceSessionID)
}

func textPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
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

// durablePollInterval paces the durable turn tail; the event log is read
// cross-process so a hot poll would only burn queries.
const durablePollInterval = 250 * time.Millisecond

// turnTerminal is the decoded control marker that ends an execution's tail.
type turnTerminal struct {
	Result string `json:"result"`
	Reason string `json:"reason"`
}

// parseTurnTerminal decodes a stored row that is a turn_terminal control
// marker, or nil for ordinary turn events. The same marker governs both the
// run tail (run-linked executions stamp run_id too) and the token tail.
func parseTurnTerminal(payload json.RawMessage) *turnTerminal {
	var probe struct {
		Type   string `json:"type"`
		Result string `json:"result"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(payload, &probe) != nil || probe.Type != "turn_terminal" {
		return nil
	}
	return &turnTerminal{Result: probe.Result, Reason: probe.Reason}
}

// waitTurnBootstrap polls until the pinned turn's start marker commits —
// yielding the canonical-history boundary a resume must reseed against — or
// the turn ends without one. Terminal is read before the boundary each round:
// start commits ahead of terminal, so a start visible at terminal time can
// never be missed by reading boundary first and done second. A turn that ends
// with no start marker (died in the claim→start gap) reports dead — its
// events are not safe to replay into an unreseeded client. The wait covers
// the claim→start gap so an early observer does not 204 on an empty log while
// an execution is legitimately running.
func (s *Server) waitTurnBootstrap(ctx context.Context, sessionID string,
	boundaryOf func(context.Context) (int64, bool, error),
	doneOf func(context.Context) (bool, error),
) (boundary int64, started, dead bool) {
	for {
		done, derr := doneOf(ctx)
		b, ok, berr := boundaryOf(ctx)
		if berr == nil && ok {
			return b, true, false
		}
		// Dead only on facts: a successful read proving terminal, plus a
		// successful read proving no start exists. A failed boundary read is
		// not evidence of absence.
		if berr == nil && derr == nil && done {
			return 0, false, true
		}
		select {
		case <-ctx.Done():
			return 0, false, false
		case <-time.After(durablePollInterval):
		}
	}
}

// streamDurableTurn tails ctx_session_event for the session's open turn —
// an open agent run when one exists, else the claimed execution lease that
// scheduler/delegate/session.send turns write under. Returns false (caller
// answers 204) only when neither exists.
func (s *Server) streamDurableTurn(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, attach sessionaccess.AttachResult, lastEventID string) (ok, truncated bool) {
	if s.sessionEvents == nil {
		return false, false
	}
	// A pinned scope ("<scope>:0") re-observes one exact turn — sent by a
	// client still holding that turn's resume state when the turn may already
	// be terminal and its execution row gone. The pin never falls through to
	// the session's current open turn: the client must see THIS turn's
	// terminal before it may adopt another. An unknown scope means the turn's
	// durable record is fully gone (pruned or never existed here) → 204 lets
	// the client release the pin and reconcile from canonical history.
	if scope, seqStr, found := strings.Cut(lastEventID, ":"); found && scope != "" {
		// Any non-negative seq pins the named turn — ":0" is just offset 0,
		// not a different identity. A malformed seq is no pin at all.
		if seq, perr := strconv.ParseInt(seqStr, 10, 64); perr == nil && seq >= 0 {
			if strings.HasPrefix(scope, "x") {
				token := scope[1:]
				_, started, berr := s.sessionEvents.TurnStartBoundary(ctx, sessionID, token)
				_, _, done, derr := s.sessionEvents.ExecutionTerminal(ctx, sessionID, token)
				// Absence is a 204 (release the pin); a read failure is not
				// absence — answer 500 so the pin survives a transient error.
				if berr != nil || derr != nil {
					writeError(w, http.StatusInternalServerError, "turn lookup failed")
					return true, false
				}
				if !started && !done {
					return false, false
				}
				return s.streamDurableExecution(ctx, w, flusher, agentID, sessionID, attach, token, lastEventID)
			}
			if _, _, err := s.sessionEvents.RunState(ctx, sessionID, scope); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return false, false
				}
				writeError(w, http.StatusInternalServerError, "turn lookup failed")
				return true, false
			}
			return s.streamDurableRun(ctx, w, flusher, agentID, sessionID, attach, scope, lastEventID)
		}
	}
	// A claimed execution is the live turn. When it carries a run id the turn
	// keeps the run's observation identity (cursor runID:seq), so a POST that
	// opened the stream and a sibling GET reconnect share one coordinate.
	// Non-run executions tail by token; only the queued-gap case — a run
	// enqueued but not yet claimed — resolves by run id alone.
	token, runID, err := s.sessionEvents.OpenExecution(ctx, sessionID)
	if err == nil && token != "" {
		if runID != "" {
			return s.streamDurableRun(ctx, w, flusher, agentID, sessionID, attach, runID, lastEventID)
		}
		return s.streamDurableExecution(ctx, w, flusher, agentID, sessionID, attach, token, lastEventID)
	}
	if runID, rerr := s.sessionEvents.OpenRunID(ctx, sessionID); rerr == nil && runID != "" {
		return s.streamDurableRun(ctx, w, flusher, agentID, sessionID, attach, runID, lastEventID)
	}
	return false, false
}

// streamDurableRun tails the event log of one known run. Split from
// streamDurableTurn so a web send can observe the run it just enqueued even
// when a fast worker already marked it terminal.
func (s *Server) streamDurableRun(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, attach sessionaccess.AttachResult, runID, lastEventID string) (bool, bool) {
	// The cursor is run-scoped ("runID:seq"). A cursor from a previous run
	// names a different sequence space: replay this run from its start — the
	// seq gap between runs is not truncation.
	var cursor int64
	if run, seqStr, found := strings.Cut(lastEventID, ":"); found {
		if run == runID {
			cursor, _ = strconv.ParseInt(seqStr, 10, 64)
		}
	} else {
		cursor, _ = strconv.ParseInt(lastEventID, 10, 64)
	}
	resumeSeq := cursor
	if cursor > 0 {
		// Truncation check: the client's cursor must reach the oldest event
		// still held for this run, otherwise events were pruned underneath it.
		minSeq, err := s.sessionEvents.MinSeqForRun(ctx, sessionID, runID)
		if err == nil && minSeq > cursor+1 {
			return false, true
		}
	}
	sctx, cancel := s.readiness.streamContext(ctx)
	defer cancel()
	// Resume metadata: the history watermark this turn started from, and the
	// message identity the stream will emit. The client reseeds its message
	// list against these before handing the stream to the SDK.
	boundary, started, dead := s.waitTurnBootstrap(sctx, sessionID,
		func(ctx context.Context) (int64, bool, error) {
			return s.sessionEvents.TurnStartBoundaryForRun(ctx, sessionID, runID)
		},
		func(ctx context.Context) (bool, error) { return s.sessionEvents.RunDone(ctx, sessionID, runID) })
	switch {
	case sctx.Err() != nil:
		return false, false
	case started:
		w.Header().Set("X-Stella-History-Before", strconv.FormatInt(boundary, 10))
		w.Header().Set("X-Stella-Message-Id", "msg-"+runID)
	case dead:
		// Terminal without a start marker: replaying without the boundary
		// would duplicate already-rendered canonical content, and even a
		// verdict-only stream makes the SDK clone the last assistant on its
		// start frame. Answer Gone — an explicit end that releases the
		// client's pin; canonical history keeps the truth.
		writeError(w, http.StatusGone, "turn ended before it could start")
		return true, false
	}

	// Replay always walks from the turn's first event: resume suppresses
	// output at or below resumeSeq but still runs the part/step transitions,
	// so a mid-part reconnect continues under the same part id.
	s.streamDurableTail(sctx, w, flusher, agentID, sessionID, attach, durableTailSpec{
		scope:     runID,
		resumeSeq: resumeSeq,
		readPage: func(ctx context.Context, afterSeq int64) ([]sessionevent.Event, error) {
			return s.sessionEvents.ReadForRun(ctx, sessionID, runID, afterSeq, 256)
		},
		done: func(ctx context.Context) (bool, error) {
			return s.sessionEvents.RunDone(ctx, sessionID, runID)
		},
		verdict: func(ctx context.Context) (string, string, error) {
			return s.sessionEvents.RunState(ctx, sessionID, runID)
		},
	})
	return true, false
}

// streamDurableExecution tails a turn that carries no agent run — scheduler,
// delegate, session.send — by the execution lease token its events are
// stamped with. The turn ends at the token's explicit turn_terminal marker,
// never at lease disappearance: finish/reap/takeover all write it in the
// same transaction that retires the row.
func (s *Server) streamDurableExecution(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, attach sessionaccess.AttachResult, token, lastEventID string) (bool, bool) {
	scope := "x" + token
	var cursor int64
	if run, seqStr, found := strings.Cut(lastEventID, ":"); found && run == scope {
		cursor, _ = strconv.ParseInt(seqStr, 10, 64)
	}
	resumeSeq := cursor
	sctx, cancel := s.readiness.streamContext(ctx)
	defer cancel()
	boundary, started, dead := s.waitTurnBootstrap(sctx, sessionID,
		func(ctx context.Context) (int64, bool, error) {
			return s.sessionEvents.TurnStartBoundary(ctx, sessionID, token)
		},
		func(ctx context.Context) (bool, error) {
			_, _, done, err := s.sessionEvents.ExecutionTerminal(ctx, sessionID, token)
			return done, err
		})
	switch {
	case sctx.Err() != nil:
		return false, false
	case started:
		w.Header().Set("X-Stella-History-Before", strconv.FormatInt(boundary, 10))
		w.Header().Set("X-Stella-Message-Id", "msg-"+scope)
	case dead:
		writeError(w, http.StatusGone, "turn ended before it could start")
		return true, false
	}

	s.streamDurableTail(sctx, w, flusher, agentID, sessionID, attach, durableTailSpec{
		scope:     scope,
		resumeSeq: resumeSeq,
		readPage: func(ctx context.Context, afterSeq int64) ([]sessionevent.Event, error) {
			return s.sessionEvents.ReadForExecution(ctx, sessionID, token, afterSeq, 256)
		},
		done: func(ctx context.Context) (bool, error) {
			_, _, done, err := s.sessionEvents.ExecutionTerminal(ctx, sessionID, token)
			return done, err
		},
		verdict: func(ctx context.Context) (string, string, error) {
			result, reason, _, err := s.sessionEvents.ExecutionTerminal(ctx, sessionID, token)
			return result, reason, err
		},
	})
	return true, false
}

// durableTailSpec names one turn's event-log coordinate and how to read its
// terminal facts. scope is both the wire bootstrap identity and each event's
// DurableID prefix, so the run tail and the token tail share one replay loop.
type durableTailSpec struct {
	scope     string
	resumeSeq int64
	readPage  func(ctx context.Context, afterSeq int64) ([]sessionevent.Event, error)
	// done reports the persisted terminal fact ahead of each page read;
	// verdict resolves the result/reason only when the log ended without an
	// explicit marker. A verdict error is never a clean finish.
	done    func(ctx context.Context) (bool, error)
	verdict func(ctx context.Context) (result, reason string, err error)
}

// streamDurableTail replays one turn's log in order and ends on its explicit
// turn_terminal marker — or, for a turn that died before writing one, on the
// persisted terminal fact confirmed before an empty page. Read failures are
// retried, never treated as drained, so a verdict the store cannot return
// cannot surface as a successful end.
func (s *Server) streamDurableTail(sctx context.Context, w http.ResponseWriter, flusher http.Flusher, agentID, sessionID string, attach sessionaccess.AttachResult, tail durableTailSpec) {
	var cursor int64
	events := make(chan agent.Event, 64)
	go func() {
		defer close(events)
		for {
			// Terminal first, page second: every turn's events commit before
			// its terminal marker/state, so a page read after observing the
			// terminal cannot miss a committed tail. The reverse order would
			// see an empty page, then a commit, then done — dropping the tail.
			done, derr := tail.done(sctx)
			page, err := tail.readPage(sctx, cursor)
			if err != nil {
				if sctx.Err() != nil {
					return
				}
				slog.WarnContext(sctx, "durable turn tail read failed", "session", sessionID, "error", err)
				select {
				case <-sctx.Done():
					return
				case <-time.After(durablePollInterval):
				}
				continue // a failed read is never "drained"
			}
			var marker *turnTerminal
			for _, row := range page {
				cursor = row.Seq
				if m := parseTurnTerminal(row.Payload); m != nil {
					marker = m
					continue
				}
				ev, decErr := agentruntime.DecodeEvent(row.Payload)
				if decErr != nil || ev.Err != nil {
					// A recorded mid-turn error is not the turn's end — the
					// persisted terminal below emits the final verdict.
					continue
				}
				ev.Seq = row.Seq
				ev.DurableID = tail.scope + ":" + strconv.FormatInt(row.Seq, 10)
				select {
				case events <- ev:
				case <-sctx.Done():
					return
				}
			}
			// The tail ends in order at the marker row, or at an empty page
			// read after the terminal already committed. A non-success
			// verdict surfaces as an error so the client does not see a clean
			// finish for a turn that failed.
			if marker != nil || (derr == nil && done && len(page) == 0) {
				var result, reason string
				if marker != nil {
					result, reason = marker.Result, marker.Reason
				} else {
					var verr error
					if result, reason, verr = tail.verdict(sctx); verr != nil {
						// The turn ended but its verdict is unreadable — a
						// blank terminal would look like a clean finish.
						slog.WarnContext(sctx, "durable turn verdict read failed", "session", sessionID, "error", verr)
						select {
						case <-sctx.Done():
							return
						case <-time.After(durablePollInterval):
						}
						continue
					}
				}
				// The durable terminal fact goes on the wire ahead of the
				// verdict error: it is the receipt that lets an observer lift
				// its history cap — stream close alone proves nothing.
				select {
				case events <- agent.Event{Terminal: &agentruntime.TurnTerminalEvent{Result: result, Reason: reason}}:
				case <-sctx.Done():
					return
				}
				if result != "" && result != "success" && result != "completed" {
					msg := result
					if reason != "" {
						msg += ": " + reason
					}
					select {
					case events <- agent.Event{Err: errors.New("turn " + msg)}:
					case <-sctx.Done():
					}
				}
				return
			}
			select {
			case <-sctx.Done():
				return
			case <-attach.Wake:
			case <-time.After(durablePollInterval):
			}
		}
	}()
	streamAgentEvents(sctx, w, flusher, agentID, sessionID, events, tail.scope, tail.resumeSeq, func() error {
		return attach.BeforeProtectedEvent(sctx)
	})
}

// sendSessionMessageDurable turns a web send into a durable run enqueue plus
// an observe-only SSE attach: any replica may execute the turn, and this
// replica streams the persisted events. The session record itself is the
// dedup point — a client-supplied Idempotency-Key maps onto request_key.
func (s *Server) sendSessionMessageDurable(w http.ResponseWriter, r *http.Request, flusher http.Flusher, authority authz.Authority, agentID, sessionID string, message agent.MessageContent, excludedTools []string) {
	prepared, err := s.sessionAccess.PrepareDurableSend(r.Context(), sessionaccess.SendInput{
		Authority:     authority,
		AgentID:       agentID,
		SessionID:     sessionID,
		Message:       message,
		ExcludedTools: excludedTools,
	})
	if err != nil {
		if errors.Is(err, session.ErrArchived) {
			writeError(w, http.StatusConflict, "session is archived; start a new session")
			return
		}
		s.writeSessionAccessError(w, err)
		return
	}
	if prepared.PlainReply != "" {
		streamPlainReply(w, flusher, prepared.PlainReply)
		return
	}
	info := prepared.Info
	requestKey := r.Header.Get("Idempotency-Key")
	if requestKey == "" {
		requestKey = uuid.Must(uuid.NewV7()).String()
	}
	input := agentrun.Input{V: agentrun.EnvelopeVersion, Kind: "message", ExcludedTools: excludedTools}
	switch m := message.(type) {
	case string:
		input.Text = m
	case []ai.ContentBlock:
		// The kind-tagged transport encoding keeps raw image bytes for the
		// executing replica; a plain json.Marshal drops every block silently.
		raw, merr := ai.MarshalTransportBlocks(m)
		if merr != nil {
			writeError(w, http.StatusInternalServerError, "encode message content")
			return
		}
		input.Content = raw
	default:
		writeError(w, http.StatusBadRequest, "unsupported message content")
		return
	}
	run, _, err := s.durableRuns.EnqueueDirect(r.Context(), agentrun.EnqueueParams{
		SessionID:  info.ID,
		AgentID:    info.AgentID,
		RequestKey: agentrun.RequestKeyRequest(requestKey),
		Actor: agentrun.Actor{
			V:        agentrun.EnvelopeVersion,
			Kind:     "user",
			UserID:   info.UserID,
			GroupID:  info.GroupID,
			GuestID:  info.GuestID,
			Platform: "web",
		},
		Input: input,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enqueue failed")
		return
	}
	// Observe: attach returns non-live (the run executes wherever a worker
	// claims it) and the durable tail streams the persisted run events.
	attach, err := s.sessionAccess.Attach(r.Context(), sessionaccess.AttachInput{Authority: authority, AgentID: agentID, SessionID: sessionID})
	if err != nil {
		s.writeSessionAccessError(w, err)
		return
	}
	defer attach.Cancel()
	if ok, _ := s.streamDurableRun(r.Context(), w, flusher, agentID, sessionID, attach, run.ID, ""); !ok {
		w.WriteHeader(http.StatusNoContent)
	}
}
