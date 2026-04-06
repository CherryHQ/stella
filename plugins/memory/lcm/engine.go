package lcm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
)

// Internal constants matching the DB schema.
const (
	kindLeaf      = "leaf"
	kindCondensed = "condensed"

	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"

	itemTypeMessage = "message"
	itemTypeSummary = "summary"

	eventTypeText       = "text"
	eventTypeMultimodal = "multimodal"
	eventTypeToolCall   = "tool_call"
	eventTypeToolResult = "tool_result"

	scopeMessages  = "messages"
	scopeSummaries = "summaries"
	scopeBoth      = "both"

	defaultFreshTail     = 20
	defaultLeafChunkSize = 10
)

// withSessionLock acquires a per-session mutex before running fn.
func (p *Provider) withSessionLock(sessionID string, fn func() error) error {
	p.globalMu.Lock()
	mu, ok := p.sessionMu[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		p.sessionMu[sessionID] = mu
	}
	p.globalMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// getOrCreateConversation retrieves or creates a conversation for the session.
// Results are cached since conversation IDs are immutable once created.
func (p *Provider) getOrCreateConversation(ctx context.Context, sessionID string) (int64, error) {
	p.globalMu.Lock()
	if id, ok := p.convCache[sessionID]; ok {
		p.globalMu.Unlock()
		return id, nil
	}
	p.globalMu.Unlock()

	conv, err := p.q.GetConversationBySessionID(ctx, sessionID)
	if err == nil {
		p.cacheConvID(sessionID, conv.ID)
		return conv.ID, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("get conversation: %w", err)
	}

	conv, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: sessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("create conversation: %w", err)
	}
	p.cacheConvID(sessionID, conv.ID)
	return conv.ID, nil
}

func (p *Provider) cacheConvID(sessionID string, convID int64) {
	p.globalMu.Lock()
	p.convCache[sessionID] = convID
	p.globalMu.Unlock()
}

// --- Message conversion ---

// toolCallEnvelope is the JSON structure stored for tool_call events.
type toolCallEnvelope struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// toolResultEnvelope is the JSON structure stored for tool_result events.
type toolResultEnvelope struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

// storageRow is a single DB row to be written for an ai.Message.
type storageRow struct {
	role      string
	eventType string
	content   string
}

// messageToRows maps an ai.Message to one or more DB rows.
// An AssistantMessage with text + tool calls produces multiple rows.
func messageToRows(msg ai.Message) []storageRow {
	switch m := msg.(type) {
	case ai.UserMessage:
		return userMessageToRows(m)
	case ai.AssistantMessage:
		return assistantMessageToRows(m)
	case ai.ToolResultMessage:
		return toolResultToRows(m)
	default:
		return nil
	}
}

func userMessageToRows(m ai.UserMessage) []storageRow {
	switch c := m.Content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: c}}
	case []ai.ContentBlock:
		data, err := json.Marshal(contentBlocksToJSON(c))
		if err != nil {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeMultimodal, content: string(data)}}
	default:
		s := fmt.Sprintf("%v", m.Content)
		if s == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: s}}
	}
}

func assistantMessageToRows(m ai.AssistantMessage) []storageRow {
	var rows []storageRow
	var text string
	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.TextContent:
			text += b.Text
		case ai.ToolCall:
			argsJSON, _ := json.Marshal(b.Arguments)
			envelope := toolCallEnvelope{ID: b.ID, Tool: b.Name, Args: argsJSON}
			data, _ := json.Marshal(envelope)
			rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeToolCall, content: string(data)})
		}
	}
	if text != "" {
		// Text row comes before tool call rows.
		rows = append([]storageRow{{role: roleAssistant, eventType: eventTypeText, content: text}}, rows...)
	}
	return rows
}

func toolResultToRows(m ai.ToolResultMessage) []storageRow {
	text := ai.FlattenText(m.Content)
	resultJSON, _ := json.Marshal(text)
	var errStr string
	if m.IsError {
		errStr = text
	}
	envelope := toolResultEnvelope{
		ID:     m.ToolCallID,
		Tool:   m.ToolName,
		Result: resultJSON,
		Error:  errStr,
	}
	data, _ := json.Marshal(envelope)
	return []storageRow{{role: roleTool, eventType: eventTypeToolResult, content: string(data)}}
}

// contentBlockJSON mirrors runner.ContentBlockJSON for storage serialization.
type contentBlockJSON struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

func contentBlocksToJSON(blocks []ai.ContentBlock) []contentBlockJSON {
	out := make([]contentBlockJSON, 0, len(blocks))
	for _, b := range blocks {
		switch b := b.(type) {
		case ai.TextContent:
			out = append(out, contentBlockJSON{Kind: "text", Text: b.Text})
		case ai.ImageContent:
			out = append(out, contentBlockJSON{Kind: "image", Data: b.Data, MimeType: b.MimeType})
		}
	}
	return out
}

// rowsToMessages merges consecutive DB rows back into ai.Messages.
// Adjacent assistant text + tool_call rows are merged into a single AssistantMessage.
func rowsToMessages(msgs []sqlc.CtxMessage) []ai.Message {
	var result []ai.Message
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		switch msg.Role {
		case roleUser:
			result = append(result, rowToUserMessage(msg))
			i++
		case roleAssistant:
			am, consumed := mergeAssistantRows(msgs, i)
			result = append(result, am)
			i += consumed
		case roleTool:
			result = append(result, rowToToolResult(msg))
			i++
		default:
			i++
		}
	}
	return result
}

func rowToUserMessage(msg sqlc.CtxMessage) ai.UserMessage {
	if msg.EventType == eventTypeMultimodal {
		var blocks []contentBlockJSON
		if json.Unmarshal([]byte(msg.Content), &blocks) == nil && len(blocks) > 0 {
			content := make([]ai.ContentBlock, 0, len(blocks))
			for _, b := range blocks {
				switch b.Kind {
				case "text":
					content = append(content, ai.TextContent{Text: b.Text})
				case "image":
					content = append(content, ai.ImageContent{Data: b.Data, MimeType: b.MimeType})
				}
			}
			return ai.UserMessage{Content: content}
		}
	}
	return ai.UserMessage{Content: msg.Content}
}

// mergeAssistantRows merges an assistant text row and any following tool_call rows
// into a single AssistantMessage. Returns the message and how many rows were consumed.
func mergeAssistantRows(msgs []sqlc.CtxMessage, start int) (ai.AssistantMessage, int) {
	var blocks []ai.ContentBlock
	consumed := 0

	msg := msgs[start]
	switch msg.EventType {
	case eventTypeText:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
	case eventTypeToolCall:
		if call, ok := decodeToolCall(msg.Content); ok {
			blocks = append(blocks, call)
		}
		consumed++
	default:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
		return ai.AssistantMessage{Content: blocks}, consumed
	}

	// Consume following tool_call rows that belong to the same assistant turn.
	for start+consumed < len(msgs) {
		next := msgs[start+consumed]
		if next.Role != roleAssistant || next.EventType != eventTypeToolCall {
			break
		}
		if call, ok := decodeToolCall(next.Content); ok {
			blocks = append(blocks, call)
		}
		consumed++
	}

	return ai.AssistantMessage{Content: blocks}, consumed
}

func decodeToolCall(content string) (ai.ToolCall, bool) {
	var env toolCallEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return ai.ToolCall{}, false
	}
	var args map[string]any
	_ = json.Unmarshal(env.Args, &args)
	return ai.ToolCall{ID: env.ID, Name: env.Tool, Arguments: args}, true
}

func rowToToolResult(msg sqlc.CtxMessage) ai.ToolResultMessage {
	var env toolResultEnvelope
	if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
		// Legacy fallback: plain text tool result.
		return ai.ToolResultMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: msg.Content}},
		}
	}
	var text string
	_ = json.Unmarshal(env.Result, &text)
	return ai.ToolResultMessage{
		ToolCallID: env.ID,
		ToolName:   env.Tool,
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		IsError:    env.Error != "",
	}
}

// estimateMessageTokens returns a rough token count for an ai.Message.
// For assistant messages, includes tool call names and arguments in the estimate.
func estimateMessageTokens(msg ai.Message) int {
	switch m := msg.(type) {
	case ai.AssistantMessage:
		total := 0
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				total += memory.EstimateTokens(b.Text)
			case ai.ToolCall:
				total += memory.EstimateTokens(b.Name)
				if b.Arguments != nil {
					data, _ := json.Marshal(b.Arguments)
					total += memory.EstimateTokens(string(data))
				}
			}
		}
		return total
	default:
		return memory.EstimateTokens(memory.MessageText(msg))
	}
}
