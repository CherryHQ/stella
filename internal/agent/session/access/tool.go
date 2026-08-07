package access

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	sessionToolName           = "session"
	defaultSessionToolPage    = 20
	maxSessionToolPage        = 100
	maxSessionToolMessagePage = 20
	maxSessionToolMessageText = 12_000
	maxSessionToolResultText  = 64_000
)

// Tool exposes read-only session discovery to an agent. Identity always comes
// from the runtime context; model arguments cannot select another owner or
// executor.
type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        sessionToolName,
		Description: "List and inspect this agent's sessions for the current user. Use delegate to create or resume isolated child sessions, and memory search to find content across session history. This tool is read-only.",
		InputSchema: tools.MustInputSchema(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["get", "list", "messages"],
      "description": "Required parameters by action: get(session_id); messages(session_id)."
    },
    "session_id": {"type": "string"},
    "kind": {
      "type": "string",
      "enum": ["main", "chat", "delegate", "task", "scheduler"]
    },
    "project_id": {"type": "string"},
    "include_archived": {"type": "boolean", "default": false},
    "page_size": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20},
    "page_token": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "default": 20},
    "skip": {"type": "integer", "minimum": 0, "default": 0}
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
	case "messages":
		result, err = executeSessionMessages(ctx, access, ident.AgentID, args)
	default:
		return "", fmt.Errorf("unknown session action %q", action)
	}
	if err != nil {
		return "", mapSessionToolError(err)
	}
	return tools.MarshalResult(result)
}

type sessionListInput struct {
	Kind            string `json:"kind,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	PageSize        int    `json:"page_size,omitempty"`
	PageToken       string `json:"page_token,omitempty"`
}

type sessionIDInput struct {
	SessionID string `json:"session_id"`
}

type sessionMessagesInput struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"`
	Skip      int    `json:"skip,omitempty"`
}

type sessionToolResponse struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Channel    string `json:"channel"`
	ProjectID  string `json:"project_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
	Archived   bool   `json:"archived"`
}

type sessionToolMessage struct {
	ID        string            `json:"id"`
	Seq       int64             `json:"seq"`
	Role      string            `json:"role"`
	EventType string            `json:"event_type,omitempty"`
	Content   string            `json:"content"`
	Parts     []toolMessagePart `json:"parts,omitempty"`
	CreatedAt string            `json:"created_at"`
	Truncated bool              `json:"truncated,omitempty"`
}

type toolMessagePart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
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
	opts := agentsession.ListOptions{
		IncludeArchived: in.IncludeArchived,
		ProjectID:       in.ProjectID,
		Offset:          offset,
	}
	if in.Kind != "" {
		kind := agentsession.Kind(in.Kind)
		if !validSessionToolKind(kind) {
			return nil, fmt.Errorf("invalid session kind %q", in.Kind)
		}
		opts.Kinds = []agentsession.Kind{kind}
	}
	page, err := access.ListPage(ctx, agentID, opts, limit)
	if err != nil {
		return nil, err
	}
	items := make([]sessionToolResponse, 0, len(page.Sessions))
	for _, info := range page.Sessions {
		items = append(items, sessionToolSummary(info))
	}
	response := map[string]any{"sessions": items}
	if page.HasMore {
		response["next_page_token"] = tools.OffsetToken(page.NextOffset)
	}
	return response, nil
}

func executeSessionGet(ctx context.Context, access *Access, agentID string, args map[string]any) (any, error) {
	var in sessionIDInput
	if err := tools.DecodeInput(args, &in, []string{"session_id"}); err != nil {
		return nil, err
	}
	info, err := access.Read(ctx, agentID, in.SessionID)
	if err != nil {
		return nil, err
	}
	return sessionToolSummary(info), nil
}

func executeSessionMessages(ctx context.Context, access *Access, agentID string, args map[string]any) (any, error) {
	var in sessionMessagesInput
	if err := tools.DecodeInput(args, &in, []string{"session_id"}); err != nil {
		return nil, err
	}
	if in.Limit == 0 {
		in.Limit = defaultSessionToolPage
	}
	if in.Limit < 1 || in.Limit > maxSessionToolMessagePage || in.Skip < 0 || in.Skip > math.MaxInt32-in.Limit-1 {
		return nil, fmt.Errorf("invalid message pagination — use limit between 1 and %d and skip of zero or greater", maxSessionToolMessagePage)
	}
	messages, err := access.ListMessages(ctx, MessageListInput{
		AgentID: agentID, SessionID: in.SessionID, Limit: in.Limit + 1, Skip: in.Skip,
	})
	if err != nil {
		return nil, err
	}
	groups := groupLogicalMessages(messages)
	hasMore := len(groups) > in.Limit
	if hasMore {
		groups = groups[len(groups)-in.Limit:]
	}
	messages = flattenMessageGroups(groups)
	items := sessionToolMessagesFrom(messages)
	response := map[string]any{"messages": items, "has_more": hasMore}
	if hasMore {
		response["next_skip"] = in.Skip + in.Limit
	}
	return response, nil
}

func sessionToolSummary(info agentsession.Info) sessionToolResponse {
	return sessionToolResponse{
		ID: info.ID, Title: info.Title, Kind: info.Kind, Channel: info.Channel, ProjectID: info.ProjectID,
		CreatedAt: info.CreatedAt.UTC().Format(time.RFC3339), LastActive: info.LastActive.UTC().Format(time.RFC3339), Archived: info.Archived,
	}
}

func validSessionToolKind(kind agentsession.Kind) bool {
	switch kind {
	case agentsession.KindMain, agentsession.KindChat, agentsession.KindDelegate, agentsession.KindTask, agentsession.KindScheduler:
		return true
	default:
		return false
	}
}

// Keep this logical-message boundary in sync with ListMessagesByLogicalPage in
// internal/db/queries/ctx_message.sql and serializeDBMessages in
// internal/server/sessions.go.
func groupLogicalMessages(messages []Message) [][]Message {
	groups := make([][]Message, 0, len(messages))
	for _, message := range messages {
		if len(groups) == 0 || message.Role != "assistant" {
			groups = append(groups, []Message{message})
			continue
		}
		last := len(groups) - 1
		previous := groups[last][len(groups[last])-1]
		if previous.Role != "assistant" {
			groups = append(groups, []Message{message})
			continue
		}
		groups[last] = append(groups[last], message)
	}
	return groups
}

func flattenMessageGroups(groups [][]Message) []Message {
	var count int
	for _, group := range groups {
		count += len(group)
	}
	messages := make([]Message, 0, count)
	for _, group := range groups {
		messages = append(messages, group...)
	}
	return messages
}

func sessionToolMessagesFrom(messages []Message) []sessionToolMessage {
	items := make([]sessionToolMessage, len(messages))
	remainingText := maxSessionToolResultText
	// The query returns chronological rows for display, but recent context is
	// more valuable when the aggregate output budget cannot hold the whole page.
	for i := len(messages) - 1; i >= 0; i-- {
		items[i] = sessionToolMessageFrom(messages[i], &remainingText)
	}
	return items
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

func sessionToolMessageFrom(message Message, remainingText *int) sessionToolMessage {
	visibleContent := message.Content
	if len(message.Parts) > 0 {
		visibleContent = visibleSessionPartText(message.Parts)
	}
	content, truncated := truncateSessionToolText(visibleContent, remainingText)
	item := sessionToolMessage{
		ID: message.ID, Seq: message.Seq, Role: message.Role, EventType: message.EventType,
		Content: content, CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339), Truncated: truncated,
	}
	for _, part := range message.Parts {
		text, partTruncated := truncateSessionToolText(part.Text, remainingText)
		item.Parts = append(item.Parts, toolMessagePart{
			Type: part.Type, Text: text, MediaID: part.MediaID, MimeType: part.MimeType, Truncated: partTruncated,
		})
	}
	return item
}

func truncateSessionToolText(text string, remaining *int) (string, bool) {
	limit := min(maxSessionToolMessageText, *remaining)
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
		return fmt.Errorf("session not found — check the id with action=list")
	case errors.Is(err, ErrForbidden):
		return fmt.Errorf("session access denied — use action=list to see sessions available to this agent")
	default:
		return err
	}
}
