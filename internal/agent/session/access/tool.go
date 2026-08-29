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
	// ListTool is the session action that lists what this agent can reach.
	// Error prose points at it, so a rename shows up here rather than in a
	// string.
	ListTool                     = "session_list"
	defaultSessionToolPage       = 20
	maxSessionToolPage           = 100
	defaultSessionTranscriptPage = 3
	maxSessionTranscriptPage     = 5
	maxSessionToolMessageText    = 12_000
	maxSessionToolOutputText     = 8_000
	maxSessionToolResultText     = agentsession.MaxSynchronousOutputBytes
	maxSessionToolPreviewText    = 12_000
	// Metadata counts too. Raise only if tool consumers demonstrate a need for
	// larger transcript pages and provider payload limits rise with it.
	maxSessionToolTranscriptBytes  = 72_000
	maxSessionToolSerializedResult = 96_000
	sessionTranscriptCursorVersion = 1
)

// Tool is one generated session action. The tool name carries the action, so
// the provider validates arguments against an exact schema before dispatch.
// Identity always comes from the runtime context; model arguments cannot select
// another principal or Agent.
type Tool struct {
	spec ActionTool
	svc  *Service
}

// NewTool builds one session action tool.
func NewTool(svc *Service, spec ActionTool) *Tool { return &Tool{spec: spec, svc: svc} }

func (t *Tool) Definition() tools.Definition { return t.spec.Definition("") }

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("session service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, t.spec.Name)
	if err != nil {
		return "", err
	}
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	access, err := t.svc.Begin(ctx, authority)
	if err != nil {
		return "", t.mapError(err)
	}
	result, err := Dispatch(ctx, sessionHandler{svc: t.svc, access: access, agentID: ident.AgentID}, t.spec.Action, args)
	if err != nil {
		return "", t.mapError(err)
	}
	output, err := tools.MarshalResult(result)
	if err == nil && len(output) > maxSessionToolSerializedResult {
		return "", fmt.Errorf("%s exceeded its serialized result limit", t.spec.Name)
	}
	return output, err
}

// sessionHandler answers the generated Dispatch. The Access is opened once per
// call from the context identity, so no handler method can widen it.
type sessionHandler struct {
	svc     *Service
	access  *Access
	agentID string
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
	MessageCount int  `json:"message_count"`
	Permanent    bool `json:"permanent"`
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

func (h sessionHandler) List(ctx context.Context, in SessionListInput) (any, error) {
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultSessionToolPage, maxSessionToolPage)
	if err != nil || offset > math.MaxInt32-limit-1 {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxSessionToolPage)
	}
	page, err := h.access.ListCardPage(ctx, h.agentID, agentsession.ListOptions{
		IncludeArchived: in.IncludeArchived != nil && *in.IncludeArchived,
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
			return nil, fmt.Errorf("session_list exceeded its serialized result limit")
		}
		items = items[:len(items)-1]
		hasMore = true
		nextOffset = offset + len(items)
	}
}

func (h sessionHandler) Get(ctx context.Context, in SessionGetInput) (any, error) {
	info, err := h.access.Read(ctx, h.agentID, in.SessionId)
	if err != nil {
		return nil, err
	}
	cards, err := h.access.projectCards(ctx, []agentsession.Info{info})
	if err != nil {
		return nil, err
	}
	contextMeta, err := h.access.ContextStats(ctx, h.agentID, in.SessionId)
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
		messages, err := h.access.ListTranscriptPage(ctx, TranscriptPageInput{
			AgentID: h.agentID, SessionID: in.SessionId, Limit: 1,
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
			Version: sessionTranscriptCursorVersion, SessionID: in.SessionId,
			AnchorSeq: anchorSeq, SnapshotSeq: anchorSeq,
		})
		return response, err
	}

	cursor, err := decodeTranscriptCursor(in.Cursor, in.SessionId)
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
	messages, err := h.access.ListTranscriptPage(ctx, TranscriptPageInput{
		AgentID: h.agentID, SessionID: in.SessionId,
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
		// Transcript pages preserve whole tool call/result pairs. A logical turn
		// whose serialized metadata exceeds the bounded tool result cannot be
		// resumed within that turn, so report the omission as permanent instead
		// of returning a cursor that would replay this exact page forever.
		page.Omitted = &sessionTranscriptOmission{MessageCount: omitted, Permanent: true}
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

func (h sessionHandler) Create(ctx context.Context, in SessionCreateInput) (any, error) {
	if strings.TrimSpace(in.Message) == "" {
		return nil, fmt.Errorf("message must not be empty")
	}
	if err := requireSynchronousSessionWait(in.Wait); err != nil {
		return nil, err
	}
	return runManagedSession(ctx, h.svc, h.agentID, "", in.Message, in.Preset)
}

func (h sessionHandler) Send(ctx context.Context, in SessionSendInput) (any, error) {
	if strings.TrimSpace(in.Message) == "" {
		return nil, fmt.Errorf("message must not be empty")
	}
	if err := requireSynchronousSessionWait(in.Wait); err != nil {
		return nil, err
	}
	if sourceID := memory.SessionIDFromContext(ctx); sourceID != "" && sourceID == in.SessionId {
		return nil, fmt.Errorf("cannot send to the current session")
	}
	info, err := h.access.Use(ctx, h.agentID, in.SessionId)
	if err != nil {
		return nil, err
	}
	if info.Archived {
		return nil, fmt.Errorf("cannot send to archived session")
	}
	switch agentsession.Kind(info.Kind) {
	case agentsession.KindDelegate:
		return runManagedSession(ctx, h.svc, h.agentID, info.ID, in.Message, "")
	case agentsession.KindMain, agentsession.KindChat:
		callCtx, err := agentctx.EnterSessionCall(ctx, memory.SessionIDFromContext(ctx), info.ID)
		if err != nil {
			return nil, err
		}
		return runConversationSession(callCtx, h.svc, info, in.Message)
	default:
		return nil, fmt.Errorf("session_send does not support control-plane sessions")
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
	truncated = truncated || result.OutputTruncated
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
	var output agentsession.OutputCollector
	var terminalErr error
	for event := range stream {
		if event.Text != "" {
			output.Write(event.Text)
		}
		if terminalErr == nil && event.Err != nil {
			terminalErr = event.Err
		}
	}
	if terminalErr != nil {
		return sessionRunResponse{SessionID: info.ID, Reply: output.String(), ReplyTruncated: output.Truncated()}, terminalErr
	}
	if err := ctx.Err(); err != nil {
		return sessionRunResponse{SessionID: info.ID, Reply: output.String(), ReplyTruncated: output.Truncated()}, err
	}
	return sessionRunResponse{SessionID: info.ID, Reply: output.String(), ReplyTruncated: output.Truncated()}, nil
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

// mapError turns an access failure into prose the model can act on. The
// recovery advice names session_list rather than the union-era "action=list".
func (t *Tool) mapError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%s: session not found — check the id with %s", t.spec.Name, ListTool)
	case errors.Is(err, ErrForbidden):
		return fmt.Errorf("%s: session access denied — use %s to see sessions available to this agent", t.spec.Name, ListTool)
	case errors.Is(err, turnqueue.ErrFull):
		return fmt.Errorf("session queue is full — retry after pending sends finish")
	case errors.Is(err, turnqueue.ErrTimeout):
		return fmt.Errorf("timed out waiting for the target session — no turn was started")
	default:
		return err
	}
}
