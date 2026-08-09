package access

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/agent/agentctx"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agent/session/turnqueue"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	sessionToolName              = "session"
	defaultSessionToolPage       = 20
	maxSessionToolPage           = 100
	defaultSessionTranscriptPage = 3
	maxSessionTranscriptPage     = 5
	maxSessionToolMessageText    = 12_000
	maxSessionToolOutputText     = 8_000
	maxSessionToolResultText     = 64_000
	maxSessionToolPreviewText    = 12_000
	// Metadata counts too. Raise only if tool consumers demonstrate a need for
	// larger transcript pages and provider payload limits rise with it.
	maxSessionToolTranscriptBytes  = 72_000
	maxSessionToolSerializedResult = 96_000
	sessionTranscriptCursorVersion = 1
)

// Tool exposes bounded Session discovery and inspection. Identity always comes
// from the runtime context; model arguments cannot select another principal or
// Agent.
type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        sessionToolName,
		Description: "List, inspect, create, and continue this agent's sessions for the current user. List returns structured recent sessions; use memory.search to recall content across sessions. Get returns session state, context statistics, and a compact preview; pass its cursor back to page a bounded raw transcript. Create starts a focused session and waits for its reply. Send continues any sendable session and waits in FIFO order when the target is busy. Agent input retains source provenance. Only wait=true is supported.",
		InputSchema: tools.MustInputSchema(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
	      "enum": ["list", "get", "create", "send"],
	      "description": "Required parameters by action: list(); get(session_id); create(message, optional preset, optional wait=true); send(session_id, message, optional wait=true)."
    },
    "session_id": {"type": "string"},
	    "message": {"type": "string", "description": "The Session's initial or next request."},
	    "preset": {"type": "string", "description": "Optional managed-Session preset for create."},
	    "wait": {"type": "boolean", "default": true, "description": "Must be true in this phase; asynchronous Session requests are not yet supported."},
    "include_archived": {"type": "boolean", "default": false},
	    "page_size": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Number of cards returned by list."},
    "page_token": {"type": "string", "description": "Opaque list cursor; pass it back unchanged."},
    "cursor": {"type": "string", "description": "Opaque get transcript cursor returned by get; pass it back unchanged."},
    "transcript_page_size": {"type": "integer", "minimum": 1, "maximum": 5, "description": "Number of whole logical turns returned by get when cursor is set."}
  },
  "required": ["action"]
}`),
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("session service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, sessionToolName)
	if err != nil {
		return "", err
	}
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapError(sessionToolName, err)
	}
	access, err := t.svc.Begin(ctx, authority)
	if err != nil {
		return "", mapSessionToolError(err)
	}
	action, err := tools.ActionArg(args, sessionToolName)
	if err != nil {
		return "", err
	}

	var result any
	switch action {
	case "list":
		result, err = executeSessionList(ctx, access, ident.AgentID, args)
	case "get":
		result, err = executeSessionGet(ctx, access, ident.AgentID, args)
	case "create":
		result, err = executeSessionCreate(ctx, t.svc, ident.AgentID, args)
	case "send":
		result, err = executeSessionSend(ctx, t.svc, access, ident.AgentID, args)
	default:
		return "", fmt.Errorf("unknown session action %q", action)
	}
	if err != nil {
		return "", mapSessionToolError(err)
	}
	output, err := tools.MarshalResult(result)
	if err == nil && len(output) > maxSessionToolSerializedResult {
		return "", fmt.Errorf("session.%s exceeded its serialized result limit", action)
	}
	return output, err
}

type sessionListInput struct {
	IncludeArchived bool   `json:"include_archived,omitempty"`
	PageSize        int    `json:"page_size,omitempty"`
	PageToken       string `json:"page_token,omitempty"`
}

type sessionGetInput struct {
	SessionID          string `json:"session_id"`
	Cursor             string `json:"cursor,omitempty"`
	TranscriptPageSize int    `json:"transcript_page_size,omitempty"`
}

type sessionCreateInput struct {
	Message string `json:"message"`
	Preset  string `json:"preset,omitempty"`
	Wait    *bool  `json:"wait,omitempty"`
}

type sessionSendInput struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Wait      *bool  `json:"wait,omitempty"`
}

type sessionRunResponse struct {
	SessionID      string `json:"session_id"`
	Reply          string `json:"reply"`
	ReplyTruncated bool   `json:"reply_truncated,omitempty"`
}

type sessionCardResponse struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	State         string `json:"state"`
	Sendable      bool   `json:"sendable"`
	LastActive    string `json:"last_active"`
	TurnStartedAt string `json:"turn_started_at,omitempty"`
}

type sessionGetResponse struct {
	sessionCardResponse
	ActiveRequest       any                     `json:"active_request"`
	LatestResult        *sessionTerminalResult  `json:"latest_result"`
	SupportedOperations []string                `json:"supported_operations"`
	ContextStats        sessionContextStats     `json:"context_stats"`
	Preview             []sessionTranscriptTurn `json:"preview,omitempty"`
	TranscriptCursor    string                  `json:"transcript_cursor,omitempty"`
	Transcript          *sessionTranscriptPage  `json:"transcript,omitempty"`
}

type sessionTerminalResult struct {
	Status      string `json:"status"`
	CompletedAt string `json:"completed_at"`
}

type sessionContextStats struct {
	MessageCount     int    `json:"message_count"`
	TokenCount       int    `json:"token_count"`
	ActiveTokenCount int    `json:"active_token_count"`
	SummaryCount     int    `json:"summary_count"`
	SummaryDepth     int    `json:"summary_depth"`
	OldestAt         string `json:"oldest_at,omitempty"`
	NewestAt         string `json:"newest_at,omitempty"`
}

type sessionTranscriptPage struct {
	Turns      []sessionTranscriptTurn    `json:"turns"`
	HasMore    bool                       `json:"has_more"`
	NextCursor string                     `json:"next_cursor,omitempty"`
	Omitted    *sessionTranscriptOmission `json:"omitted,omitempty"`
}

type sessionTranscriptOmission struct {
	MessageCount int    `json:"message_count"`
	Cursor       string `json:"cursor"`
}

type sessionTranscriptTurn struct {
	Messages []sessionToolMessage `json:"messages"`
}

type sessionToolMessage struct {
	ID              string            `json:"id"`
	Seq             int64             `json:"seq"`
	Role            string            `json:"role"`
	Type            string            `json:"type"`
	Content         string            `json:"content,omitempty"`
	ToolCallID      string            `json:"tool_call_id,omitempty"`
	ToolName        string            `json:"tool_name,omitempty"`
	Parts           []toolMessagePart `json:"parts,omitempty"`
	CreatedAt       string            `json:"created_at"`
	Truncated       bool              `json:"truncated,omitempty"`
	ActorType       string            `json:"actor_type"`
	ActorID         string            `json:"actor_id,omitempty"`
	SourceSessionID string            `json:"source_session_id,omitempty"`
}

type toolMessagePart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type transcriptCursor struct {
	Version     int    `json:"v"`
	SessionID   string `json:"session_id"`
	AnchorSeq   int64  `json:"anchor_seq,omitempty"`
	SnapshotSeq int64  `json:"snapshot_seq,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}

func executeSessionList(ctx context.Context, access *Access, agentID string, args map[string]any) (any, error) {
	var in sessionListInput
	if err := tools.DecodeInput(args, &in, nil); err != nil {
		return nil, err
	}
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultSessionToolPage, maxSessionToolPage)
	if err != nil || offset > math.MaxInt32-limit-1 {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxSessionToolPage)
	}
	page, err := access.ListCardPage(ctx, agentID, agentsession.ListOptions{
		IncludeArchived: in.IncludeArchived,
		Offset:          offset,
	}, limit)
	if err != nil {
		return nil, err
	}
	items := make([]sessionCardResponse, 0, len(page.Sessions))
	for _, card := range page.Sessions {
		items = append(items, sessionCardFrom(card))
	}
	hasMore := page.HasMore
	nextOffset := page.NextOffset
	for {
		response := map[string]any{"sessions": items}
		if hasMore {
			response["next_page_token"] = tools.OffsetToken(nextOffset)
		}
		serialized, marshalErr := tools.MarshalResult(response)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(serialized) <= maxSessionToolSerializedResult {
			return response, nil
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("session.list exceeded its serialized result limit")
		}
		items = items[:len(items)-1]
		hasMore = true
		nextOffset = offset + len(items)
	}
}

func executeSessionGet(ctx context.Context, access *Access, agentID string, args map[string]any) (any, error) {
	var in sessionGetInput
	if err := tools.DecodeInput(args, &in, []string{"session_id"}); err != nil {
		return nil, err
	}
	info, err := access.Read(ctx, agentID, in.SessionID)
	if err != nil {
		return nil, err
	}
	cards, err := access.projectCards(ctx, []agentsession.Info{info})
	if err != nil {
		return nil, err
	}
	contextMeta, err := access.ContextStats(ctx, agentID, in.SessionID)
	if err != nil {
		return nil, err
	}
	response := sessionGetResponse{
		sessionCardResponse: sessionCardFrom(cards[0]),
		ActiveRequest:       nil, // Session Requests arrive in Phase 3+.
		SupportedOperations: []string{"get"},
		ContextStats:        sessionContextStatsFrom(contextMeta),
	}
	if cards[0].Sendable {
		response.SupportedOperations = append(response.SupportedOperations, "send")
	}
	if info.LastTurnResult.Valid() && !info.LastTurnCompletedAt.IsZero() {
		response.LatestResult = &sessionTerminalResult{
			Status: string(info.LastTurnResult), CompletedAt: info.LastTurnCompletedAt.UTC().Format(time.RFC3339),
		}
	}

	if in.Cursor == "" {
		messages, err := access.ListTranscriptPage(ctx, TranscriptPageInput{
			AgentID: agentID, SessionID: in.SessionID, Limit: 1,
		})
		if err != nil {
			return nil, err
		}
		response.Preview = sessionTranscriptTurns(messages, maxSessionToolPreviewText)
		var anchorSeq int64
		for _, message := range messages {
			anchorSeq = max(anchorSeq, message.Seq)
		}
		if anchorSeq == 0 {
			return response, nil
		}
		response.TranscriptCursor, err = encodeTranscriptCursor(transcriptCursor{
			Version: sessionTranscriptCursorVersion, SessionID: in.SessionID,
			AnchorSeq: anchorSeq, SnapshotSeq: anchorSeq,
		})
		return response, err
	}

	cursor, err := decodeTranscriptCursor(in.Cursor, in.SessionID)
	if err != nil {
		return nil, err
	}
	pageSize := in.TranscriptPageSize
	if pageSize == 0 {
		pageSize = defaultSessionTranscriptPage
	}
	if pageSize < 1 || pageSize > maxSessionTranscriptPage || cursor.Offset > math.MaxInt32-pageSize-1 {
		return nil, fmt.Errorf("invalid transcript pagination — use page_size between 1 and %d and pass cursor unchanged", maxSessionTranscriptPage)
	}
	messages, err := access.ListTranscriptPage(ctx, TranscriptPageInput{
		AgentID: agentID, SessionID: in.SessionID,
		AnchorSeq: cursor.AnchorSeq, SnapshotSeq: cursor.SnapshotSeq,
		Offset: cursor.Offset, Limit: pageSize + 1,
	})
	if err != nil {
		return nil, err
	}
	groups := groupLogicalTurns(messages)
	hasMore := len(groups) > pageSize
	if hasMore {
		groups = groups[len(groups)-pageSize:]
	}
	turns, omitted := boundedSessionTranscriptTurns(groups, maxSessionToolResultText, maxSessionToolTranscriptBytes)
	page := &sessionTranscriptPage{Turns: turns, HasMore: hasMore}
	if hasMore {
		cursor.Offset += pageSize
		page.NextCursor, err = encodeTranscriptCursor(cursor)
		if err != nil {
			return nil, err
		}
	}
	if omitted > 0 {
		page.Omitted = &sessionTranscriptOmission{MessageCount: omitted, Cursor: in.Cursor}
	}
	response.Transcript = page
	return response, nil
}

func sessionContextStatsFrom(meta ContextMeta) sessionContextStats {
	out := sessionContextStats{
		MessageCount: meta.MessageCount, TokenCount: meta.SourceTokenCount,
		ActiveTokenCount: meta.ActiveTokenCount, SummaryCount: meta.SummaryCount,
		SummaryDepth: meta.SummaryDepth,
	}
	if meta.OldestAt != nil {
		out.OldestAt = meta.OldestAt.UTC().Format(time.RFC3339)
	}
	if meta.NewestAt != nil {
		out.NewestAt = meta.NewestAt.UTC().Format(time.RFC3339)
	}
	return out
}

func executeSessionCreate(ctx context.Context, svc *Service, agentID string, args map[string]any) (any, error) {
	var in sessionCreateInput
	if err := tools.DecodeInput(args, &in, []string{"message"}); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, fmt.Errorf("message must not be empty")
	}
	if err := requireSynchronousSessionWait(in.Wait); err != nil {
		return nil, err
	}
	return runManagedSession(ctx, svc, agentID, "", in.Message, in.Preset)
}

func executeSessionSend(ctx context.Context, svc *Service, access *Access, agentID string, args map[string]any) (any, error) {
	var in sessionSendInput
	if err := tools.DecodeInput(args, &in, []string{"session_id", "message"}); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, fmt.Errorf("message must not be empty")
	}
	if err := requireSynchronousSessionWait(in.Wait); err != nil {
		return nil, err
	}
	if sourceID := memory.SessionIDFromContext(ctx); sourceID != "" && sourceID == in.SessionID {
		return nil, fmt.Errorf("cannot send to the current session")
	}
	info, err := access.Use(ctx, agentID, in.SessionID)
	if err != nil {
		return nil, err
	}
	if info.Archived {
		return nil, fmt.Errorf("cannot send to archived session")
	}
	switch agentsession.Kind(info.Kind) {
	case agentsession.KindDelegate:
		return runManagedSession(ctx, svc, agentID, info.ID, in.Message, "")
	case agentsession.KindMain, agentsession.KindChat:
		callCtx, err := agentctx.EnterSessionCall(ctx, memory.SessionIDFromContext(ctx), info.ID)
		if err != nil {
			return nil, err
		}
		return runConversationSession(callCtx, svc, info, in.Message)
	default:
		return nil, fmt.Errorf("session.send does not support control-plane sessions")
	}
}

func requireSynchronousSessionWait(wait *bool) error {
	if wait != nil && !*wait {
		return fmt.Errorf("session wait=false is not yet supported; use wait=true")
	}
	return nil
}

func runManagedSession(ctx context.Context, svc *Service, agentID, sessionID, message, preset string) (any, error) {
	runtime, err := svc.runtimeFor(agentID)
	if err != nil {
		return nil, err
	}
	result, err := runtime.RunManagedSession(ctx, delegatetool.ManagedSessionRequest{
		SessionID: sessionID,
		Message:   message,
		Preset:    preset,
	})
	if err != nil {
		return nil, err
	}
	reply, truncated := tools.TruncateText(result.Output, maxSessionToolResultText)
	return sessionRunResponse{SessionID: result.SessionID, Reply: reply, ReplyTruncated: truncated}, nil
}

func runConversationSession(ctx context.Context, svc *Service, info agentsession.Info, message string) (any, error) {
	runtime, err := svc.runtimeFor(info.AgentID)
	if err != nil {
		return nil, err
	}
	// This stream is consumed only into the synchronous tool result. A target
	// Session's channel is execution context, not an authorized connector route;
	// no channel publisher participates in this path.
	stream := runtime.RunConversationSession(ctx, info, message)
	var output strings.Builder
	for event := range stream {
		if event.Text != "" {
			output.WriteString(event.Text)
		}
		if event.Err != nil {
			return nil, event.Err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply, truncated := tools.TruncateText(output.String(), maxSessionToolResultText)
	return sessionRunResponse{SessionID: info.ID, Reply: reply, ReplyTruncated: truncated}, nil
}

func sessionCardFrom(card Card) sessionCardResponse {
	response := sessionCardResponse{
		ID: card.ID, Title: card.Title, Summary: card.Summary, State: card.State,
		Sendable: card.Sendable, LastActive: card.LastActive.UTC().Format(time.RFC3339),
	}
	if !card.TurnStartedAt.IsZero() {
		response.TurnStartedAt = card.TurnStartedAt.UTC().Format(time.RFC3339)
	}
	return response
}

func encodeTranscriptCursor(cursor transcriptCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode transcript cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeTranscriptCursor(value, sessionID string) (transcriptCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return transcriptCursor{}, fmt.Errorf("invalid transcript cursor — pass a cursor returned by get unchanged")
	}
	var cursor transcriptCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != sessionTranscriptCursorVersion || cursor.SessionID != sessionID || cursor.AnchorSeq < 0 || cursor.SnapshotSeq < 0 || cursor.Offset < 0 ||
		(cursor.SnapshotSeq > 0 && cursor.AnchorSeq > cursor.SnapshotSeq) {
		return transcriptCursor{}, fmt.Errorf("invalid transcript cursor — pass a cursor returned by get unchanged")
	}
	return cursor, nil
}

func groupLogicalTurns(messages []Message) [][]Message {
	groups := make([][]Message, 0)
	for _, message := range messages {
		if len(groups) == 0 || message.Role == "user" {
			groups = append(groups, []Message{message})
			continue
		}
		last := len(groups) - 1
		groups[last] = append(groups[last], message)
	}
	return groups
}

func sessionTranscriptTurns(messages []Message, budget int) []sessionTranscriptTurn {
	turns, _ := boundedSessionTranscriptTurns(groupLogicalTurns(messages), budget, maxSessionToolTranscriptBytes)
	return turns
}

func boundedSessionTranscriptTurns(groups [][]Message, textBudget, byteBudget int) ([]sessionTranscriptTurn, int) {
	remainingText, remainingBytes := textBudget, byteBudget
	selected := make([]sessionTranscriptTurn, 0, len(groups))
	omitted := 0
	stopped := false
	for groupIndex := len(groups) - 1; groupIndex >= 0; groupIndex-- {
		chunks := sessionMessageChunks(groups[groupIndex])
		selectedMessages := make([]indexedSessionToolMessage, 0, len(groups[groupIndex]))
		for chunkIndex := len(chunks) - 1; chunkIndex >= 0; chunkIndex-- {
			if stopped || remainingText <= 0 {
				stopped = true
				omitted += len(chunks[chunkIndex].messages)
				continue
			}
			chunkText := remainingText
			projected := make([]indexedSessionToolMessage, 0, len(chunks[chunkIndex].messages))
			projectedMessages := make([]sessionToolMessage, 0, len(chunks[chunkIndex].messages))
			for i, message := range chunks[chunkIndex].messages {
				toolMessage := sessionToolMessageFrom(message, &chunkText)
				projected = append(projected, indexedSessionToolMessage{
					index: chunks[chunkIndex].indices[i], message: toolMessage,
				})
				projectedMessages = append(projectedMessages, toolMessage)
			}
			encoded, err := json.Marshal(projectedMessages)
			if err != nil || len(encoded)+1 > remainingBytes {
				stopped = true
				omitted += len(chunks[chunkIndex].messages)
				continue
			}
			remainingText = chunkText
			remainingBytes -= len(encoded) + 1
			selectedMessages = append(selectedMessages, projected...)
		}
		if len(selectedMessages) > 0 {
			slices.SortFunc(selectedMessages, func(a, b indexedSessionToolMessage) int { return a.index - b.index })
			messages := make([]sessionToolMessage, 0, len(selectedMessages))
			for _, selected := range selectedMessages {
				messages = append(messages, selected.message)
			}
			selected = append(selected, sessionTranscriptTurn{Messages: messages})
		}
	}
	slices.Reverse(selected)
	return selected, omitted
}

type indexedSessionToolMessage struct {
	index   int
	message sessionToolMessage
}

type sessionMessageChunk struct {
	messages []Message
	indices  []int
	latest   int
}

func sessionMessageChunks(messages []Message) []sessionMessageChunk {
	resultByCallID := make(map[string]int)
	for i, message := range messages {
		if id := storedToolResultID(message); id != "" {
			resultByCallID[id] = i
		}
	}
	pairedResults := make(map[int]struct{})
	chunks := make([]sessionMessageChunk, 0, len(messages))
	for i, message := range messages {
		if _, paired := pairedResults[i]; paired {
			continue
		}
		if id := storedToolCallID(message); id != "" {
			if resultIndex, ok := resultByCallID[id]; ok {
				chunks = append(chunks, sessionMessageChunk{
					messages: []Message{message, messages[resultIndex]}, indices: []int{i, resultIndex}, latest: max(i, resultIndex),
				})
				pairedResults[resultIndex] = struct{}{}
				continue
			}
		}
		chunks = append(chunks, sessionMessageChunk{messages: []Message{message}, indices: []int{i}, latest: i})
	}
	slices.SortFunc(chunks, func(a, b sessionMessageChunk) int { return a.latest - b.latest })
	return chunks
}

func storedToolCallID(message Message) string {
	if message.EventType != "tool_call" {
		return ""
	}
	var call storedToolCall
	if json.Unmarshal([]byte(message.Content), &call) != nil {
		return ""
	}
	return call.ID
}

func storedToolResultID(message Message) string {
	if message.EventType != "tool_result" {
		return ""
	}
	var result storedToolResult
	if json.Unmarshal([]byte(message.Content), &result) != nil {
		return ""
	}
	return result.ID
}

func visibleSessionPartText(parts []MessagePart) string {
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n")
}

type storedToolCall struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type storedToolResult struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func sessionToolMessageFrom(message Message, remaining *int) sessionToolMessage {
	visibleContent := message.Content
	itemType := message.EventType
	toolCallID, toolName := "", ""
	perMessageLimit := maxSessionToolMessageText

	switch message.EventType {
	case "tool_call":
		var call storedToolCall
		if json.Unmarshal([]byte(message.Content), &call) == nil {
			toolCallID, toolName, visibleContent = call.ID, call.Tool, string(call.Args)
		}
	case "tool_result":
		perMessageLimit = maxSessionToolOutputText
		var result storedToolResult
		if json.Unmarshal([]byte(message.Content), &result) == nil {
			toolCallID, toolName = result.ID, result.Tool
			visibleContent = rawJSONText(result.Result)
			if visibleContent == "" {
				visibleContent = result.Error
			}
		}
	}
	if len(message.Parts) > 0 {
		visibleContent = visibleSessionPartText(message.Parts)
	}
	content, truncated := truncateSessionToolText(visibleContent, remaining, perMessageLimit)
	item := sessionToolMessage{
		ID: message.ID, Seq: message.Seq, Role: message.Role, Type: itemType,
		Content: content, ToolCallID: toolCallID, ToolName: toolName,
		CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339), Truncated: truncated,
		ActorType: message.ActorType, ActorID: message.ActorID, SourceSessionID: message.SourceSessionID,
	}
	for _, part := range message.Parts {
		text, partTruncated := truncateSessionToolText(part.Text, remaining, perMessageLimit)
		item.Parts = append(item.Parts, toolMessagePart{
			Type: part.Type, Text: text, MediaID: part.MediaID, MimeType: part.MimeType, Truncated: partTruncated,
		})
	}
	return item
}

func rawJSONText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func truncateSessionToolText(text string, remaining *int, perMessageLimit int) (string, bool) {
	limit := min(perMessageLimit, *remaining)
	if limit <= 0 {
		return "", text != ""
	}
	value, truncated := tools.TruncateText(text, limit)
	*remaining -= len(value)
	return value, truncated
}

func mapSessionToolError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("session not found — check the id with action=find")
	case errors.Is(err, ErrForbidden):
		return fmt.Errorf("session access denied — use action=find to see sessions available to this agent")
	case errors.Is(err, turnqueue.ErrFull):
		return fmt.Errorf("session queue is full — retry after pending sends finish")
	case errors.Is(err, turnqueue.ErrTimeout):
		return fmt.Errorf("timed out waiting for the target session — no turn was started")
	default:
		return err
	}
}
