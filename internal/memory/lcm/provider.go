package lcm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Compile-time interface checks.
var (
	_ memory.Provider        = (*Provider)(nil)
	_ memory.Compactor       = (*Provider)(nil)
	_ memory.Searcher        = (*Provider)(nil)
	_ memory.Explorer        = (*Provider)(nil)
	_ memory.ProfileStore    = (*Provider)(nil)
	_ memory.SessionManager  = (*Provider)(nil)
	_ memory.Reviewer        = (*Provider)(nil)
	_ memory.ChangelogWriter = (*Provider)(nil)
	_ memory.ChangelogReader = (*Provider)(nil)
	_ memory.ConstraintStore = (*Provider)(nil)
)

// Provider implements memory.Provider and all six capability interfaces
// using the lossless context management algorithm.
type Provider struct {
	db         *sql.DB
	q          *sqlc.Queries
	assembler  *assembler
	compaction *compactionEngine
	retrieval  *retrievalEngine
	summarizer memory.Summarizer
	sessionMu  map[string]*sync.Mutex
	globalMu   sync.Mutex
	freshTail  int
	log        *slog.Logger
}

// New creates a new LCM provider.
// summarizerFn provides LLM access for compaction; if nil, a deterministic
// truncation fallback is used.
// cfg is the plugin-specific configuration from the plugin.config JSON.
func New(db *sql.DB, summarizerFn func(ctx context.Context, prompt string) (string, error), cfg map[string]any) (*Provider, error) {
	q := sqlc.New(db)

	freshTail := defaultFreshTail
	if v, ok := cfg["fresh_tail"]; ok {
		if n, ok := toInt(v); ok && n > 0 {
			freshTail = n
		}
	}

	var summarizer memory.Summarizer
	if summarizerFn != nil {
		summarizer = &memory.LLMSummarizer{Generate: summarizerFn}
	} else {
		summarizer = &memory.StaticSummarizer{Response: ""}
	}

	p := &Provider{
		db:        db,
		q:         q,
		assembler: newAssembler(q, slog.Default()),
		retrieval: newRetrievalEngine(q),
		sessionMu: make(map[string]*sync.Mutex),
		freshTail: freshTail,
		log:       slog.Default(),
	}
	p.summarizer = summarizer
	p.compaction = newCompactionEngine(db, q, summarizer, freshTail)
	return p, nil
}

// Name implements memory.Provider.
func (p *Provider) Name() string { return "lcm" }

// Bootstrap implements memory.Provider.
func (p *Provider) Bootstrap(ctx context.Context, session memory.Session) error {
	_, err := p.getOrCreateConversation(ctx, session)
	return err
}

// Append implements memory.Provider.
func (p *Provider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	return p.withSessionLock(session.ID, func() error {
		convID, err := p.getOrCreateConversation(ctx, session)
		if err != nil {
			return err
		}

		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		qtx := p.q.WithTx(tx)

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
					ID:             uuid.NewString(),
					ConversationID: convID,
					Seq:            seq,
					Role:           row.role,
					EventType:      row.eventType,
					Content:        row.content,
					TokenCount:     int64(memory.EstimateTokens(row.content)),
				})
				if err != nil {
					return fmt.Errorf("create message: %w", err)
				}

				ordinal++
				err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
					ConversationID: convID,
					Ordinal:        ordinal,
					ItemType:       itemTypeMessage,
					MessageID:      sql.NullString{String: dbMsg.ID, Valid: true},
				})
				if err != nil {
					return fmt.Errorf("append context item: %w", err)
				}
			}
		}

		return tx.Commit()
	})
}

// Assemble implements memory.Provider.
func (p *Provider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		return nil, err
	}
	return p.assembler.assemble(ctx, convID, budget, freshTail)
}

// Stats implements memory.Provider.
func (p *Provider) Stats(ctx context.Context, session memory.Session) (memory.SessionStats, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return memory.SessionStats{}, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.SessionStats{}, nil
	}
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get conversation: %w", err)
	}

	msgCount, err := p.q.GetMessageCount(ctx, conv.ID)
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get message count: %w", err)
	}

	tokenCount, err := p.q.GetContextTokenCount(ctx, conv.ID)
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get token count: %w", err)
	}

	summaries, err := p.q.GetSummariesByConversation(ctx, conv.ID)
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get summaries: %w", err)
	}

	stats := memory.SessionStats{
		MessageCount: int(msgCount),
		TokenCount:   int(tokenCount),
		SummaryCount: len(summaries),
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err == nil && len(msgs) > 0 {
		stats.OldestAt = parseTime(msgs[0].CreatedAt)
		stats.NewestAt = parseTime(msgs[len(msgs)-1].CreatedAt)
	}

	return stats, nil
}

// Close implements memory.Provider.
func (p *Provider) Close() error {
	return nil
}

// NeedsCompaction implements memory.Compactor.
func (p *Provider) NeedsCompaction(ctx context.Context, session memory.Session, threshold float64) bool {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return false
	}
	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if err != nil {
		return false
	}
	tokens, err := p.q.GetContextTokenCount(ctx, conv.ID)
	if err != nil || float64(tokens) <= threshold {
		return false
	}
	items, err := p.q.GetContextItems(ctx, conv.ID)
	if err != nil {
		return false
	}
	return p.hasCompactableRun(ctx, items)
}

func (p *Provider) hasCompactableRun(ctx context.Context, items []sqlc.CtxItem) bool {
	_, older := splitFreshTail(items, p.freshTail)
	if len(findMessageRuns(older, defaultLeafChunkSize)) > 0 {
		return true
	}

	depthOf := make(map[string]int64)
	for _, item := range items {
		if item.ItemType != itemTypeSummary || !item.SummaryID.Valid {
			continue
		}
		sum, err := p.q.GetSummary(ctx, sqlc.GetSummaryParams{ID: item.SummaryID.String, ConversationID: item.ConversationID})
		if err != nil {
			return false
		}
		depthOf[item.SummaryID.String] = sum.Depth
	}
	return len(findSummaryRuns(items, 2, depthOf)) > 0
}

// Compact implements memory.Compactor.
func (p *Provider) Compact(ctx context.Context, session memory.Session, mode memory.CompactionMode) (*memory.CompactionResult, error) {
	var result *memory.CompactionResult
	err := p.withSessionLock(session.ID, func() error {
		convID, cErr := p.getOrCreateConversation(ctx, session)
		if cErr != nil {
			return cErr
		}
		var compErr error
		result, compErr = p.compaction.compact(ctx, convID, mode)
		return compErr
	})
	return result, err
}

// toInt converts a value from JSON config to int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// parseTime parses a SQLite datetime string.
func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseNullTime parses a sql.NullString time field into *time.Time.
func parseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	if t.IsZero() {
		return nil
	}
	return &t
}
