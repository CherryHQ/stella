package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vaayne/anna/agent/runner"
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

// Ingest persists a single RPCEvent and appends a context item.
func (e *engine) Ingest(ctx context.Context, sessionID string, evt runner.RPCEvent) error {
	return e.withSessionLock(sessionID, func() error {
		convID, err := e.getOrCreateConversation(ctx, sessionID)
		if err != nil {
			return err
		}
		return e.ingestEvent(ctx, convID, evt)
	})
}

// IngestBatch persists multiple RPCEvents within a single transaction.
func (e *engine) IngestBatch(ctx context.Context, sessionID string, evts []runner.RPCEvent) error {
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

		for _, evt := range evts {
			role, eventType, content := eventToMessage(evt)
			if role == "" || content == "" {
				continue
			}

			seq++
			msg, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
				ConversationID: convID,
				Seq:            seq,
				Role:           role,
				EventType:      eventType,
				Content:        content,
				TokenCount:     int64(EstimateTokens(content)),
			})
			if err != nil {
				return fmt.Errorf("create message: %w", err)
			}

			ordinal++
			err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
				ConversationID: convID,
				Ordinal:        ordinal,
				ItemType:       ItemTypeMessage,
				MessageID:      sql.NullInt64{Int64: msg.ID, Valid: true},
			})
			if err != nil {
				return fmt.Errorf("append context item: %w", err)
			}
		}

		return tx.Commit()
	})
}

// Assemble builds context for the model within the token budget.
func (e *engine) Assemble(ctx context.Context, sessionID string, budget int, freshTail int) ([]runner.RPCEvent, error) {
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

// Load returns the full event history for a session as RPCEvents.
// Returns nil, nil for non-existent sessions.
func (e *engine) Load(ctx context.Context, sessionID string) ([]runner.RPCEvent, error) {
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

	var result []runner.RPCEvent
	for _, msg := range msgs {
		evts := messageToRPCEvents(msg)
		result = append(result, evts...)
	}
	return result, nil
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

// ingestEvent converts an RPCEvent to a message and appends a context item.
// Both operations are wrapped in a transaction for atomicity.
func (e *engine) ingestEvent(ctx context.Context, convID int64, evt runner.RPCEvent) error {
	role, eventType, content := eventToMessage(evt)
	if role == "" || content == "" {
		return nil // skip events we cannot map
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
	seq++

	msg, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: convID,
		Seq:            seq,
		Role:           role,
		EventType:      eventType,
		Content:        content,
		TokenCount:     int64(EstimateTokens(content)),
	})
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}

	ordinal, err := qtx.GetMaxContextOrdinal(ctx, convID)
	if err != nil {
		return fmt.Errorf("get max ordinal: %w", err)
	}
	ordinal++

	err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        ordinal,
		ItemType:       ItemTypeMessage,
		MessageID:      sql.NullInt64{Int64: msg.ID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("append context item: %w", err)
	}

	return tx.Commit()
}

// eventToMessage maps an RPCEvent to (role, eventType, content) for storage.
func eventToMessage(evt runner.RPCEvent) (role, eventType, content string) {
	switch evt.Type {
	case runner.RPCEventUserMessage:
		if len(evt.Content) > 0 {
			return RoleUser, EventTypeMultimodal, string(evt.Content)
		}
		return RoleUser, EventTypeText, evt.Summary

	case runner.RPCEventMessageUpdate:
		return RoleAssistant, EventTypeText, evt.Summary

	case runner.RPCEventToolCall:
		envelope := toolCallEnvelope{
			ID:   evt.ID,
			Tool: evt.Tool,
			Args: evt.Result, // Result field carries args JSON for tool_call events
		}
		data, _ := json.Marshal(envelope)
		return RoleAssistant, EventTypeToolCall, string(data)

	case runner.RPCEventToolResult:
		envelope := toolResultEnvelope{
			ID:     evt.ID,
			Tool:   evt.Tool,
			Result: evt.Result,
			Error:  evt.Error,
		}
		data, _ := json.Marshal(envelope)
		return RoleTool, EventTypeToolResult, string(data)

	default:
		return "", "", ""
	}
}
