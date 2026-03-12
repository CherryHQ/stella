package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/db/sqlc"
)

// engine is the concrete implementation of the Engine interface.
type engine struct {
	db         *sql.DB
	q          *sqlc.Queries
	assembler  *Assembler
	compaction *CompactionEngine
	retrieval  *RetrievalEngine
	sessionMu  map[string]*sync.Mutex
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

// NewEngine creates a new LCM engine backed by a SQLite database at dbPath.
func NewEngine(dbPath string, summarizer Summarizer, opts ...EngineOption) (Engine, error) {
	db, err := OpenDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("lcm: open db: %w", err)
	}

	q := sqlc.New(db)
	e := &engine{
		db:        db,
		q:         q,
		assembler: NewAssembler(q),
		retrieval: NewRetrievalEngine(q),
		sessionMu: make(map[string]*sync.Mutex),
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

// IngestBatch persists multiple RPCEvents.
func (e *engine) IngestBatch(ctx context.Context, sessionID string, evts []runner.RPCEvent) error {
	return e.withSessionLock(sessionID, func() error {
		convID, err := e.getOrCreateConversation(ctx, sessionID)
		if err != nil {
			return err
		}
		for _, evt := range evts {
			if err := e.ingestEvent(ctx, convID, evt); err != nil {
				return err
			}
		}
		return nil
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

// Close releases database resources.
func (e *engine) Close() error {
	return e.db.Close()
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
func (e *engine) getOrCreateConversation(ctx context.Context, sessionID string) (int64, error) {
	conv, err := e.q.GetConversationBySessionID(ctx, sessionID)
	if err == nil {
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
	return conv.ID, nil
}

// ingestEvent converts an RPCEvent to a message and appends a context item.
func (e *engine) ingestEvent(ctx context.Context, convID int64, evt runner.RPCEvent) error {
	role, content := eventToRoleContent(evt)
	if role == "" || content == "" {
		return nil // skip events we cannot map
	}

	seq, err := e.q.GetMaxSeq(ctx, convID)
	if err != nil {
		return fmt.Errorf("get max seq: %w", err)
	}
	seq++

	msg, err := e.q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: convID,
		Seq:            seq,
		Role:           role,
		Content:        content,
		TokenCount:     int64(EstimateTokens(content)),
	})
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}

	ordinal, err := e.q.GetMaxContextOrdinal(ctx, convID)
	if err != nil {
		return fmt.Errorf("get max ordinal: %w", err)
	}
	ordinal++

	err = e.q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        ordinal,
		ItemType:       ItemTypeMessage,
		MessageID:      sql.NullInt64{Int64: msg.ID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("append context item: %w", err)
	}

	return nil
}

// eventToRoleContent maps an RPCEvent to a (role, content) pair.
func eventToRoleContent(evt runner.RPCEvent) (string, string) {
	switch evt.Type {
	case runner.RPCEventUserMessage:
		return RoleUser, evt.Summary

	case runner.RPCEventMessageUpdate:
		return RoleAssistant, evt.Summary

	case runner.RPCEventToolCall:
		content := evt.Tool
		if len(evt.Result) > 0 {
			content += ": " + string(evt.Result)
		}
		return RoleAssistant, content

	case runner.RPCEventToolResult:
		if len(evt.Result) > 0 {
			return RoleTool, string(evt.Result)
		}
		return RoleTool, ""

	default:
		return "", ""
	}
}
