package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vaayne/anna/ai"
	"github.com/vaayne/anna/db/sqlc"
)

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

// engine is the concrete implementation of the Engine interface.
type engine struct {
	db         *sql.DB
	q          *sqlc.Queries
	assembler  *Assembler
	compaction *CompactionEngine
	retrieval  *RetrievalEngine
	sessionMu  map[string]*sync.Mutex
	convCache  map[string]int64 // sessionID → conversation ID (immutable once created)
	globalMu   sync.Mutex
	freshTail  int
	log        *slog.Logger
}

// Compile-time interface check.
var _ Engine = (*engine)(nil)

// EngineOption configures the engine.
type EngineOption func(*engine)

// WithFreshTail sets the number of recent messages protected from compaction.
func WithFreshTail(n int) EngineOption {
	return func(e *engine) {
		if n > 0 {
			e.freshTail = n
		}
	}
}

// WithLogger sets the logger for the engine.
func WithLogger(log *slog.Logger) EngineOption {
	return func(e *engine) {
		e.log = log
	}
}

// NewEngine creates a new memory engine backed by a SQLite database at dbPath.
func NewEngine(dbPath string, summarizer Summarizer, opts ...EngineOption) (Engine, error) {
	db, err := OpenDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}

	q := sqlc.New(db)
	e := &engine{
		db:        db,
		q:         q,
		assembler: NewAssembler(q),
		retrieval: NewRetrievalEngine(q),
		sessionMu: make(map[string]*sync.Mutex),
		convCache: make(map[string]int64),
		freshTail: DefaultFreshTail,
		log:       slog.Default(),
	}

	for _, opt := range opts {
		opt(e)
	}

	e.compaction = NewCompactionEngine(db, q, summarizer, e.freshTail)
	return e, nil
}

// Bootstrap ensures a conversation exists for the given session ID.
func (e *engine) Bootstrap(ctx context.Context, sessionID string) error {
	_, err := e.getOrCreateConversation(ctx, sessionID)
	return err
}

// Ingest persists a single ai.Message and appends context items.
func (e *engine) Ingest(ctx context.Context, sessionID string, msg ai.Message) error {
	return e.withSessionLock(sessionID, func() error {
		convID, err := e.getOrCreateConversation(ctx, sessionID)
		if err != nil {
			return err
		}
		return e.ingestMessage(ctx, convID, msg)
	})
}

// IngestBatch persists multiple ai.Messages within a single transaction.
func (e *engine) IngestBatch(ctx context.Context, sessionID string, msgs []ai.Message) error {
	return e.withSessionLock(sessionID, func() error {
		convID, err := e.getOrCreateConversation(ctx, sessionID)
		if err != nil {
			return err
		}

		tx, err := e.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		qtx := e.q.WithTx(tx)

		seq, err := qtx.GetMaxSeq(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max seq: %w", err)
		}
		ordinal, err := qtx.GetMaxContextOrdinal(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max ordinal: %w", err)
		}

		for _, msg := range msgs {
			rows := messageToRows(msg)
			for _, row := range rows {
				seq++
				dbMsg, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
					ConversationID: convID,
					Seq:            seq,
					Role:           row.role,
					EventType:      row.eventType,
					Content:        row.content,
					TokenCount:     int64(EstimateTokens(row.content)),
				})
				if err != nil {
					return fmt.Errorf("create message: %w", err)
				}

				ordinal++
				err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
					ConversationID: convID,
					Ordinal:        ordinal,
					ItemType:       ItemTypeMessage,
					MessageID:      sql.NullInt64{Int64: dbMsg.ID, Valid: true},
				})
				if err != nil {
					return fmt.Errorf("append context item: %w", err)
				}
			}
		}

		return tx.Commit()
	})
}

// Assemble builds context for the model within the token budget.
func (e *engine) Assemble(ctx context.Context, sessionID string, budget int, freshTail int) ([]ai.Message, error) {
	convID, err := e.getOrCreateConversation(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return e.assembler.Assemble(ctx, convID, budget, freshTail)
}

// Compact runs compaction passes on the conversation.
func (e *engine) Compact(ctx context.Context, sessionID string, mode CompactionMode) (*CompactionResult, error) {
	var result *CompactionResult
	err := e.withSessionLock(sessionID, func() error {
		convID, cErr := e.getOrCreateConversation(ctx, sessionID)
		if cErr != nil {
			return cErr
		}
		var compErr error
		result, compErr = e.compaction.Compact(ctx, convID, mode)
		return compErr
	})
	return result, err
}

// NeedsCompaction checks if the context token count exceeds the given threshold.
// The threshold is treated as an absolute token limit (e.g., MaxTokens from config).
func (e *engine) NeedsCompaction(ctx context.Context, sessionID string, threshold float64) bool {
	conv, err := e.q.GetConversationBySessionID(ctx, sessionID)
	if err != nil {
		return false
	}
	tokens, err := e.q.GetContextTokenCount(ctx, conv.ID)
	if err != nil {
		return false
	}
	return float64(tokens) > threshold
}

// Retrieval returns the retrieval engine.
func (e *engine) Retrieval() *RetrievalEngine {
	return e.retrieval
}

// SaveInfo persists session metadata. Creates a new conversation or updates
// the title, channel, archived, and last_active fields of an existing one.
func (e *engine) SaveInfo(ctx context.Context, info SessionInfo) error {
	conv, err := e.q.GetConversationBySessionID(ctx, info.ID)
	if err == sql.ErrNoRows {
		lastActive := info.LastActive
		if lastActive.IsZero() {
			lastActive = time.Now().UTC()
		}
		_, err = e.q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
			SessionID:  info.ID,
			Title:      sql.NullString{String: info.Title, Valid: info.Title != ""},
			Channel:    info.Channel,
			Archived:   boolToInt(info.Archived),
			LastActive: lastActive.UTC().Format("2006-01-02 15:04:05"),
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	// Update existing conversation fields.
	if info.Title != "" {
		if err := e.q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
			Title:     sql.NullString{String: info.Title, Valid: true},
			SessionID: info.ID,
		}); err != nil {
			return fmt.Errorf("update title: %w", err)
		}
	}
	if boolToInt(info.Archived) != conv.Archived {
		if err := e.q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
			Archived:  boolToInt(info.Archived),
			SessionID: info.ID,
		}); err != nil {
			return fmt.Errorf("update archived: %w", err)
		}
	}
	if err := e.q.UpdateConversationLastActive(ctx, info.ID); err != nil {
		return fmt.Errorf("update last_active: %w", err)
	}
	return nil
}

// LoadInfo retrieves session metadata by session ID.
func (e *engine) LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error) {
	conv, err := e.q.GetConversationBySessionID(ctx, sessionID)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("get conversation: %w", err)
	}
	return convToSessionInfo(conv), nil
}

// ListInfo lists session metadata ordered by last_active descending.
func (e *engine) ListInfo(ctx context.Context, includeArchived bool) ([]SessionInfo, error) {
	var convs []sqlc.Conversation
	var err error
	if includeArchived {
		convs, err = e.q.ListConversationsAll(ctx)
	} else {
		convs, err = e.q.ListConversations(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	result := make([]SessionInfo, len(convs))
	for i, c := range convs {
		result[i] = convToSessionInfo(c)
	}
	return result, nil
}

// Load returns the full event history for a session as ai.Messages.
// Returns nil, nil for non-existent sessions.
func (e *engine) Load(ctx context.Context, sessionID string) ([]ai.Message, error) {
	conv, err := e.q.GetConversationBySessionID(ctx, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := e.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	return rowsToMessages(msgs), nil
}

// Close releases database resources.
func (e *engine) Close() error {
	return e.db.Close()
}

func convToSessionInfo(conv sqlc.Conversation) SessionInfo {
	info := SessionInfo{
		ID:       conv.SessionID,
		Channel:  conv.Channel,
		Archived: conv.Archived != 0,
	}
	if conv.Title.Valid {
		info.Title = conv.Title.String
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.CreatedAt); err == nil {
		info.CreatedAt = t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.LastActive); err == nil {
		info.LastActive = t
	}
	return info
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// withSessionLock acquires a per-session mutex before running fn.
func (e *engine) withSessionLock(sessionID string, fn func() error) error {
	e.globalMu.Lock()
	mu, ok := e.sessionMu[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		e.sessionMu[sessionID] = mu
	}
	e.globalMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// getOrCreateConversation retrieves or creates a conversation for the session.
// Results are cached since conversation IDs are immutable once created.
func (e *engine) getOrCreateConversation(ctx context.Context, sessionID string) (int64, error) {
	e.globalMu.Lock()
	if id, ok := e.convCache[sessionID]; ok {
		e.globalMu.Unlock()
		return id, nil
	}
	e.globalMu.Unlock()

	conv, err := e.q.GetConversationBySessionID(ctx, sessionID)
	if err == nil {
		e.cacheConvID(sessionID, conv.ID)
		return conv.ID, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("get conversation: %w", err)
	}

	conv, err = e.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: sessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("create conversation: %w", err)
	}
	e.cacheConvID(sessionID, conv.ID)
	return conv.ID, nil
}

func (e *engine) cacheConvID(sessionID string, convID int64) {
	e.globalMu.Lock()
	e.convCache[sessionID] = convID
	e.globalMu.Unlock()
}

// ingestMessage converts an ai.Message to DB rows and appends context items.
// All operations are wrapped in a transaction for atomicity.
func (e *engine) ingestMessage(ctx context.Context, convID int64, msg ai.Message) error {
	rows := messageToRows(msg)
	if len(rows) == 0 {
		return nil
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := e.q.WithTx(tx)

	seq, err := qtx.GetMaxSeq(ctx, convID)
	if err != nil {
		return fmt.Errorf("get max seq: %w", err)
	}
	ordinal, err := qtx.GetMaxContextOrdinal(ctx, convID)
	if err != nil {
		return fmt.Errorf("get max ordinal: %w", err)
	}

	for _, row := range rows {
		seq++
		dbMsg, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
			ConversationID: convID,
			Seq:            seq,
			Role:           row.role,
			EventType:      row.eventType,
			Content:        row.content,
			TokenCount:     int64(EstimateTokens(row.content)),
		})
		if err != nil {
			return fmt.Errorf("create message: %w", err)
		}

		ordinal++
		err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
			ConversationID: convID,
			Ordinal:        ordinal,
			ItemType:       ItemTypeMessage,
			MessageID:      sql.NullInt64{Int64: dbMsg.ID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("append context item: %w", err)
		}
	}

	return tx.Commit()
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
		return []storageRow{{role: RoleUser, eventType: EventTypeText, content: c}}
	case []ai.ContentBlock:
		data, err := json.Marshal(contentBlocksToJSON(c))
		if err != nil {
			return nil
		}
		return []storageRow{{role: RoleUser, eventType: EventTypeMultimodal, content: string(data)}}
	default:
		s := fmt.Sprintf("%v", m.Content)
		if s == "" {
			return nil
		}
		return []storageRow{{role: RoleUser, eventType: EventTypeText, content: s}}
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
			rows = append(rows, storageRow{role: RoleAssistant, eventType: EventTypeToolCall, content: string(data)})
		}
	}
	if text != "" {
		// Text row comes before tool call rows.
		rows = append([]storageRow{{role: RoleAssistant, eventType: EventTypeText, content: text}}, rows...)
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
	return []storageRow{{role: RoleTool, eventType: EventTypeToolResult, content: string(data)}}
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
func rowsToMessages(msgs []sqlc.Message) []ai.Message {
	var result []ai.Message
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		switch msg.Role {
		case RoleUser:
			result = append(result, rowToUserMessage(msg))
			i++
		case RoleAssistant:
			am, consumed := mergeAssistantRows(msgs, i)
			result = append(result, am)
			i += consumed
		case RoleTool:
			result = append(result, rowToToolResult(msg))
			i++
		default:
			i++
		}
	}
	return result
}

func rowToUserMessage(msg sqlc.Message) ai.UserMessage {
	if msg.EventType == EventTypeMultimodal {
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
func mergeAssistantRows(msgs []sqlc.Message, start int) (ai.AssistantMessage, int) {
	var blocks []ai.ContentBlock
	consumed := 0

	msg := msgs[start]
	switch msg.EventType {
	case EventTypeText:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
	case EventTypeToolCall:
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
		if next.Role != RoleAssistant || next.EventType != EventTypeToolCall {
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

func rowToToolResult(msg sqlc.Message) ai.ToolResultMessage {
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
