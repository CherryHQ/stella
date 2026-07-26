package lcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/renderrefs"
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
	eventTypeThinking   = "thinking"
	eventTypeMultimodal = "multimodal"
	eventTypeToolCall   = "tool_call"
	eventTypeToolResult = "tool_result"

	scopeMessages  = "messages"
	scopeSummaries = "summaries"
	scopeBoth      = "both"

	// defaultFreshTail is the number of recent user turns preserved verbatim.
	defaultFreshTail          = 6
	defaultLeafChunkSize      = 10
	oversizedToolResultTokens = 2000
	sessionLockStripes        = 64
)

// withSessionLock acquires a deterministic striped mutex before running fn. This
// is an in-process fast-path only: it collapses same-process contention before it
// reaches the database. Cross-node correctness rests on the transaction-scoped
// LockConversationForWrite advisory lock taken inside the write tx (Append and the
// compaction writeback), not on this mutex.
func (p *Provider) withSessionLock(sessionID string, fn func() error) error {
	mu := &p.sessionLocks[sessionLockStripe(sessionID)]
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

func sessionLockStripe(sessionID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return h.Sum32() % sessionLockStripes
}

// getOrCreateConversation retrieves or creates a scoped conversation for the session.
func (p *Provider) getOrCreateConversation(ctx context.Context, session memory.Session) (string, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return "", err
	}

	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if err == nil {
		return conv.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("get conversation: %w", err)
	}

	now := time.Now().UTC()
	conv, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID:         uuid.Must(uuid.NewV7()).String(),
		SessionID:  session.ID,
		Channel:    session.Channel,
		Kind:       "chat",
		AgentID:    pgnull.Text(session.AgentID),
		UserID:     pgtype.Text{String: session.UserID, Valid: true},
		GroupID:    pgnull.Text(session.GroupID),
		LastActive: now,
	})
	if err == nil {
		return conv.ID, nil
	}
	// Lost the create race: another writer (possibly another node) inserted this
	// conversation between our GetConversationBySessionID miss and this INSERT.
	// session_id is globally unique (ctx_conversation_session_id_key), so the
	// scoped re-read returns their row. Defining the race out of existence beats
	// surfacing a spurious 23505 to every Append/Assemble caller.
	//
	// Only that one constraint: ctx_conversation also carries the one-active-
	// session-per-binding indexes (idx_one_agent_main, idx_one_agent_group_chat),
	// and a violation of those is a real invariant breach with no row to re-read.
	// Treating it as a lost race would hide it behind a "no rows" error.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "ctx_conversation_session_id_key" {
		conv, err = p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
		if err != nil {
			return "", fmt.Errorf("get conversation after create race: %w", err)
		}
		return conv.ID, nil
	}
	return "", fmt.Errorf("create conversation: %w", err)
}

// --- Message conversion ---

// toolCallEnvelope is the JSON structure stored for tool_call events.
type toolCallEnvelope struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// toolResultEnvelope is the JSON structure stored for tool_result events.
// Blocks is populated only for multimodal results (e.g. images); it carries the
// exact content blocks so a reloaded transcript reproduces byte-identical input
// and the provider's prompt cache stays valid. Result remains the flattened text
// for backward compatibility and token estimation.
type toolResultEnvelope struct {
	ID         string                 `json:"id"`
	Tool       string                 `json:"tool"`
	Result     json.RawMessage        `json:"result"`
	Error      string                 `json:"error,omitempty"`
	Blocks     []contentBlockJSON     `json:"blocks,omitempty"`
	References []renderrefs.Reference `json:"references,omitempty"`
}

// storageRow is a single DB row to be written for an ai.Message.
type storageRow struct {
	role      string
	eventType string
	content   string
}

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
	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.ThinkingContent:
			if b.Thinking != "" {
				rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeThinking, content: b.Thinking})
			}
		case ai.TextContent:
			if b.Text != "" {
				rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeText, content: b.Text})
			}
		case ai.ToolCall:
			argsJSON, _ := json.Marshal(b.Arguments)
			envelope := toolCallEnvelope{ID: b.ID, Tool: b.Name, Args: argsJSON}
			data, _ := json.Marshal(envelope)
			rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeToolCall, content: string(data)})
		}
	}
	return rows
}

func toolResultToRows(m ai.ToolResultMessage) []storageRow {
	// Runner is the single extraction chokepoint, so the common path arrives with
	// references already on the message and a clean body; scrubRenderableRefs is a
	// no-op there. The fallback only fires for a legacy/direct tool result that
	// reached memory with a raw sentinel still in some text block — per block, so
	// the cleaning also covers the image path's Blocks below, not just the text.
	content, fallbackRefs := scrubRenderableRefs(m.Content)
	refs := mergeReferences(m.References, fallbackRefs)
	text := ai.FlattenText(content)
	resultJSON, _ := json.Marshal(text)
	var errStr string
	if m.IsError {
		errStr = text
	}
	envelope := toolResultEnvelope{
		ID:         m.ToolCallID,
		Tool:       m.ToolName,
		Result:     resultJSON,
		Error:      errStr,
		References: refs,
	}
	if ai.HasImage(content) {
		envelope.Blocks = contentBlocksToJSON(content)
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

// scrubRenderableRefs lifts renderable-reference sentinels out of every text
// block, returning the cleaned blocks plus the references found. It extracts per
// block (not over the space-joined flatten) so a sentinel in any block — not just
// the first — is recovered, and so the cleaned blocks can back both the text body
// and the persisted image Blocks. Blocks are copied lazily, so the common already-
// clean path returns the input slice untouched.
func scrubRenderableRefs(blocks []ai.ContentBlock) ([]ai.ContentBlock, []renderrefs.Reference) {
	var refs []renderrefs.Reference
	out := blocks
	copied := false
	for i, b := range blocks {
		tc, ok := b.(ai.TextContent)
		if !ok {
			continue
		}
		clean, extracted := renderrefs.Extract(tc.Text)
		if clean == tc.Text && len(extracted) == 0 {
			continue
		}
		if !copied {
			out = append([]ai.ContentBlock(nil), blocks...)
			copied = true
		}
		tc.Text = clean
		out[i] = tc
		refs = append(refs, extracted...)
	}
	return out, refs
}

// mergeReferences appends src onto dst, skipping references already present by
// (type, id), so a sentinel that survives in both the envelope and the raw text
// is not double-counted.
func mergeReferences(dst, src []renderrefs.Reference) []renderrefs.Reference {
	if len(src) == 0 {
		return dst
	}
	seen := make(map[[2]string]struct{}, len(dst)+len(src))
	for _, r := range dst {
		seen[[2]string{r.Type, r.ID}] = struct{}{}
	}
	for _, r := range src {
		key := [2]string{r.Type, r.ID}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		dst = append(dst, r)
	}
	return dst
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

// contentBlocksFromJSON is the inverse of contentBlocksToJSON. It returns nil
// when there are no decodable blocks, letting callers fall back to a text path.
func contentBlocksFromJSON(blocks []contentBlockJSON) []ai.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	content := make([]ai.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		switch b.Kind {
		case "text":
			content = append(content, ai.TextContent{Text: b.Text})
		case "image":
			content = append(content, ai.ImageContent{Data: b.Data, MimeType: b.MimeType})
		}
	}
	return content
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

func rowsToReviewMessages(msgs []sqlc.CtxMessage) []memory.ReviewMessage {
	result := make([]memory.ReviewMessage, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		switch msg.Role {
		case roleUser:
			result = append(result, memory.ReviewMessage{
				ID:       msg.ID,
				FirstSeq: msg.Seq,
				LastSeq:  msg.Seq,
				Message:  rowToUserMessage(msg),
			})
			i++
		case roleAssistant:
			am, consumed := mergeAssistantRows(msgs, i)
			if am.Timestamp.IsZero() {
				am.Timestamp = msg.CreatedAt.UTC()
			}
			last := msgs[i+consumed-1]
			result = append(result, memory.ReviewMessage{
				ID:       msg.ID,
				FirstSeq: msg.Seq,
				LastSeq:  last.Seq,
				Message:  am,
			})
			i += consumed
		case roleTool:
			tm := rowToToolResult(msg)
			if tm.Timestamp.IsZero() {
				tm.Timestamp = msg.CreatedAt.UTC()
			}
			result = append(result, memory.ReviewMessage{
				ID:       msg.ID,
				FirstSeq: msg.Seq,
				LastSeq:  msg.Seq,
				Message:  tm,
			})
			i++
		default:
			i++
		}
	}
	return result
}

func rowToUserMessage(msg sqlc.CtxMessage) ai.UserMessage {
	ts := msg.CreatedAt.UTC()
	if msg.EventType == eventTypeMultimodal {
		var blocks []contentBlockJSON
		if json.Unmarshal([]byte(msg.Content), &blocks) == nil {
			if content := contentBlocksFromJSON(blocks); content != nil {
				return ai.UserMessage{Content: content, Timestamp: ts}
			}
		}
	}
	return ai.UserMessage{Content: msg.Content, Timestamp: ts}
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
	case eventTypeThinking:
		blocks = append(blocks, ai.ThinkingContent{Thinking: msg.Content})
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

	// Consume following rows that belong to the same assistant turn.
	for start+consumed < len(msgs) {
		next := msgs[start+consumed]
		if next.Role != roleAssistant {
			break
		}
		switch next.EventType {
		case eventTypeThinking:
			blocks = append(blocks, ai.ThinkingContent{Thinking: next.Content})
		case eventTypeText:
			blocks = append(blocks, ai.TextContent{Text: next.Content})
		case eventTypeToolCall:
			if call, ok := decodeToolCall(next.Content); ok {
				blocks = append(blocks, call)
			}
		default:
			return ai.AssistantMessage{Content: blocks}, consumed
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
		slog.Warn("corrupt tool result row: content is not valid JSON", "msg_id", msg.ID, "error", err)
		return ai.ToolResultMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: msg.Content}},
		}
	}
	content := contentBlocksFromJSON(env.Blocks)
	if content == nil {
		var text string
		_ = json.Unmarshal(env.Result, &text)
		content = []ai.ContentBlock{ai.TextContent{Text: text}}
	}
	return ai.ToolResultMessage{
		ToolCallID: env.ID,
		ToolName:   env.Tool,
		Content:    content,
		IsError:    env.Error != "",
		References: env.References,
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
