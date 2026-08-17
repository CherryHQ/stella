package lcm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Compile-time interface checks.
var (
	_ memory.Provider              = (*Provider)(nil)
	_ memory.InboxAppender         = (*Provider)(nil)
	_ memory.Compactor             = (*Provider)(nil)
	_ memory.Searcher              = (*Provider)(nil)
	_ memory.Explorer              = (*Provider)(nil)
	_ memory.ProfileStore          = (*Provider)(nil)
	_ memory.SessionManager        = (*Provider)(nil)
	_ memory.ReviewHistoryReader   = (*Provider)(nil)
	_ memory.Reviewer              = (*Provider)(nil)
	_ memory.ChangelogWriter       = (*Provider)(nil)
	_ memory.ChangelogReader       = (*Provider)(nil)
	_ memory.ChangelogPageReader   = (*Provider)(nil)
	_ memory.ConstraintStore       = (*Provider)(nil)
	_ memory.KnowledgeUsageTracker = (*Provider)(nil)
)

// Provider implements memory.Provider and all six capability interfaces
// using the lossless context management algorithm.
type Provider struct {
	db           *pgxpool.Pool
	q            *sqlc.Queries
	assembler    *assembler
	compaction   *compactionEngine
	retrieval    *retrievalEngine
	summarizer   memory.Summarizer
	sessionLocks [sessionLockStripes]sync.Mutex
	freshTail    int
	log          *slog.Logger
}

// Option configures optional Provider behavior at construction.
type Option func(*Provider)

// WithQueryEmbedder enables the semantic search lane: search() embeds the query
// with emb and fuses vector KNN hits with the BM25 lexical results. Without it
// (the default) search stays pure lexical. A nil emb is ignored so callers can
// pass an unconditionally-built option.
func WithQueryEmbedder(emb QueryEmbedder) Option {
	return func(p *Provider) {
		if emb != nil {
			p.retrieval.embedder = emb
		}
	}
}

// New creates a new LCM provider.
// summarizerFn provides LLM access for compaction; if nil, compaction is disabled.
// cfg is the plugin-specific configuration from the plugin.config JSON.
func New(db *pgxpool.Pool, summarizerFn func(ctx context.Context, prompt string) (string, error), cfg map[string]any, opts ...Option) (*Provider, error) {
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
	}

	p := &Provider{
		db:        db,
		q:         q,
		assembler: newAssembler(q, slog.Default()),
		retrieval: newRetrievalEngine(q, slog.Default()),
		freshTail: freshTail,
		log:       slog.Default(),
	}
	p.summarizer = summarizer
	p.compaction = newCompactionEngine(db, q, summarizer, freshTail)
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// Name implements memory.Provider.
func (p *Provider) Name() string { return "lcm" }

// Bootstrap implements memory.Provider.
func (p *Provider) Bootstrap(ctx context.Context, session memory.Session) error {
	_, err := p.getOrCreateConversation(ctx, session)
	return err
}

// Append implements the sole durable-write contract. Ordinary sessions accept
// only canonical references; deferred groups retain the legacy inline codec.
func (p *Provider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	rows := make([]storageRow, 0, len(msgs))
	for _, msg := range msgs {
		if session.GroupID != "" {
			rows = append(rows, messageToRows(msg)...)
			continue
		}
		canonical, err := canonicalMessageToRows(msg)
		if err != nil {
			return fmt.Errorf("canonical message: %w", err)
		}
		rows = append(rows, canonical...)
	}
	if len(rows) == 0 {
		return p.withSessionLock(session.ID, func() error {
			_, err := p.getOrCreateConversation(ctx, session)
			return err
		})
	}
	return p.appendRows(ctx, session, rows, nil)
}

// AppendInboxInput atomically claims one durable Session inbox row and appends
// its text input. The inbox facts are runtime-authored, so every immutable fact
// is rechecked at the write boundary before the model can observe success.
func (p *Provider) AppendInboxInput(ctx context.Context, session memory.Session, inboxID string, msg ai.Message) error {
	if inboxID == "" {
		return errors.New("append inbox input: empty inbox ID")
	}
	if session.GroupID != "" {
		return errors.New("append inbox input: group sessions are not supported")
	}
	userMsg, ok := msg.(ai.UserMessage)
	if !ok {
		return fmt.Errorf("append inbox input: got %T, want ai.UserMessage", msg)
	}
	content, ok := userMsg.Content.(string)
	if !ok {
		return errors.New("append inbox input: content must be text")
	}
	actor, ok := eventlog.MessageActorFromContext(ctx)
	if !ok || actor.Type != eventlog.ActorAgent || actor.SourceSessionID == "" {
		return errors.New("append inbox input: trusted agent provenance is required")
	}
	rows, err := canonicalMessageToRows(msg)
	if err != nil {
		return fmt.Errorf("canonical inbox message: %w", err)
	}
	if len(rows) != 1 || rows[0].role != roleUser {
		return errors.New("append inbox input: expected one canonical user row")
	}
	return p.appendRows(ctx, session, rows, &inboxClaim{
		id:              inboxID,
		sourceSessionID: actor.SourceSessionID,
		targetSessionID: session.ID,
		actorID:         actor.ID,
		content:         content,
	})
}

var errCanonicalMediaUnavailable = errors.New("canonical media unavailable")

func validateCanonicalMedia(ctx context.Context, q *sqlc.Queries, userID string, rows []storageRow) error {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, row := range rows {
		for _, part := range row.parts {
			if part.partType != "image" {
				continue
			}
			if _, ok := seen[part.mediaID]; ok {
				continue
			}
			seen[part.mediaID] = struct{}{}
			ids = append(ids, part.mediaID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	media, err := q.ListMediaByIDsForUser(ctx, sqlc.ListMediaByIDsForUserParams{
		UserID:   userID,
		MediaIds: ids,
	})
	if err != nil {
		return fmt.Errorf("validate canonical media: %w", err)
	}
	owned := make(map[string]struct{}, len(media))
	for _, item := range media {
		owned[item.ID] = struct{}{}
	}
	for _, row := range rows {
		for _, part := range row.parts {
			if part.partType != "image" {
				continue
			}
			if _, ok := owned[part.mediaID]; !ok {
				return errCanonicalMediaUnavailable
			}
		}
	}
	return nil
}

func createMessagePart(ctx context.Context, q *sqlc.Queries, messageID string, ordinal int64, part messagePartRow) error {
	params := sqlc.CreateMessagePartParams{
		ID:        uuid.Must(uuid.NewV7()).String(),
		MessageID: messageID,
		PartType:  part.partType,
		Ordinal:   ordinal,
	}
	switch part.partType {
	case "text":
		params.TextContent = pgtype.Text{String: part.text, Valid: true}
	case "image":
		params.MediaID = pgtype.Text{String: part.mediaID, Valid: true}
		params.TextContent = pgtype.Text{String: part.text, Valid: true}
	default:
		return fmt.Errorf("create message part: unsupported type %q", part.partType)
	}
	if _, err := q.CreateMessagePart(ctx, params); err != nil {
		return fmt.Errorf("create message part: %w", err)
	}
	return nil
}

type inboxClaim struct {
	id              string
	sourceSessionID string
	targetSessionID string
	actorID         string
	content         string
}

func (p *Provider) appendRows(ctx context.Context, session memory.Session, rows []storageRow, claim *inboxClaim) error {
	if len(rows) == 0 {
		return nil
	}
	return p.withSessionLock(session.ID, func() error {
		session, err := requireMemorySessionScope(ctx, session)
		if err != nil {
			return err
		}
		convID, err := p.getOrCreateConversation(ctx, session)
		if err != nil {
			return err
		}

		tx, err := p.db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		qtx := p.q.WithTx(tx)

		// Serialize the seq/ordinal read-modify-write for this conversation across
		// nodes. The in-process striped mutex above only covers one process; under
		// PostgreSQL a second node would read the same GetMaxSeq and collide on
		// ctx_message(conversation_id, seq). Released with the tx.
		if err = qtx.LockConversationForWrite(ctx, convID); err != nil {
			return fmt.Errorf("lock conversation: %w", err)
		}
		if err = agentrun.ValidateTx(ctx, tx); err != nil {
			return err
		}
		if claim != nil {
			guard, _ := agentrun.GuardFromContext(ctx)
			_, err = qtx.ClaimSessionInboxDelivery(ctx, sqlc.ClaimSessionInboxDeliveryParams{
				ID:              claim.id,
				SourceSessionID: claim.sourceSessionID,
				TargetSessionID: claim.targetSessionID,
				ActorID:         claim.actorID,
				Content:         claim.content,
				RunID:           pgtype.Text{String: guard.RunID, Valid: true},
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return memory.ErrInboxNotPending
			}
			if err != nil {
				return fmt.Errorf("claim session inbox: %w", err)
			}
		}

		seq, err := qtx.GetMaxSeq(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max seq: %w", err)
		}
		ordinal, err := qtx.GetMaxContextOrdinal(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max ordinal: %w", err)
		}
		if err := validateCanonicalMedia(ctx, qtx, session.UserID, rows); err != nil {
			return err
		}

		for rowIndex, row := range rows {
			seq++
			actor := actorForStorageRow(ctx, session, row)
			inboxID := pgtype.Text{}
			if claim != nil && rowIndex == 0 {
				inboxID = pgtype.Text{String: claim.id, Valid: true}
			}
			dbMsg, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
				ID:              uuid.Must(uuid.NewV7()).String(),
				ConversationID:  convID,
				Seq:             seq,
				Role:            row.role,
				EventType:       row.eventType,
				Content:         row.content,
				TokenCount:      int64(memory.EstimateTokens(row.tokenText)),
				ActorType:       string(actor.Type),
				ActorID:         pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
				SourceSessionID: pgtype.Text{String: actor.SourceSessionID, Valid: actor.SourceSessionID != ""},
				InboxID:         inboxID,
			})
			if err != nil {
				return fmt.Errorf("create message: %w", err)
			}
			for partOrdinal, part := range row.parts {
				if err := createMessagePart(ctx, qtx, dbMsg.ID, int64(partOrdinal), part); err != nil {
					return err
				}
			}

			ordinal++
			err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
				ConversationID: convID,
				Ordinal:        ordinal,
				ItemType:       itemTypeMessage,
				MessageID:      pgtype.Text{String: dbMsg.ID, Valid: true},
				EventType:      row.eventType,
				Role:           row.role,
			})
			if err != nil {
				return fmt.Errorf("append context item: %w", err)
			}
		}
		if claim != nil {
			if err := qtx.UpdateConversationLastActive(ctx, sqlc.UpdateConversationLastActiveParams{
				SessionID: session.ID,
				UserID:    pgtype.Text{String: session.UserID, Valid: session.UserID != ""},
				AgentID:   pgtype.Text{String: session.AgentID, Valid: session.AgentID != ""},
			}); err != nil {
				return fmt.Errorf("touch inbox conversation: %w", err)
			}
		}

		if err := tx.Commit(ctx); err != nil {
			if claim != nil {
				return fmt.Errorf("%w: %w", memory.ErrInboxAppendOutcomeUnknown, err)
			}
			return err
		}
		return nil
	})
}

func actorForStorageRow(ctx context.Context, session memory.Session, row storageRow) eventlog.MessageActor {
	if row.role == roleUser {
		if actor, ok := eventlog.MessageActorFromContext(ctx); ok {
			return actor
		}
		// Direct provider callers are human-history import paths. Unknown legacy
		// identity may remain NULL, but the actor type is never inferred from text.
		return eventlog.MessageActor{Type: eventlog.ActorHuman, ID: session.UserID}
	}
	return eventlog.MessageActor{Type: eventlog.ActorAgent, ID: session.AgentID}
}

// Assemble implements memory.Provider.
func (p *Provider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	if session.GroupID != "" {
		return p.assembleGroup(ctx, session, budget, freshTail)
	}
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
	if errors.Is(err, pgx.ErrNoRows) {
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

	bounds, err := p.q.GetConversationTimeBounds(ctx, conv.ID)
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get conversation time bounds: %w", err)
	}
	stats.OldestAt = parseTime(sqlTimeString(bounds.EarliestAt))
	stats.NewestAt = parseTime(sqlTimeString(bounds.LatestAt))

	return stats, nil
}

// Close implements memory.Provider.
func (p *Provider) Close() error {
	return nil
}

// NeedsCompaction implements memory.Compactor.
func (p *Provider) NeedsCompaction(ctx context.Context, session memory.Session, threshold float64) bool {
	if p.summarizer == nil {
		return false
	}
	if session.GroupID != "" {
		return false
	}
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

	depthOf, err := p.summaryDepths(ctx, items)
	if err != nil {
		return false
	}
	return len(findSummaryRuns(items, 2, depthOf)) > 0
}

func (p *Provider) summaryDepths(ctx context.Context, items []sqlc.CtxItem) (map[string]int64, error) {
	byConversation := make(map[string][]string)
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.ItemType != itemTypeSummary || !item.SummaryID.Valid {
			continue
		}
		key := item.ConversationID + "\x00" + item.SummaryID.String
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		byConversation[item.ConversationID] = append(byConversation[item.ConversationID], item.SummaryID.String)
	}

	depthOf := make(map[string]int64)
	for convID, ids := range byConversation {
		summaries, err := p.q.ListSummariesByIDs(ctx, sqlc.ListSummariesByIDsParams{ConversationID: convID, SummaryIds: ids})
		if err != nil {
			return nil, err
		}
		for _, sum := range summaries {
			depthOf[sum.ID] = sum.Depth
		}
		for _, id := range ids {
			if _, ok := depthOf[id]; !ok {
				return nil, pgx.ErrNoRows
			}
		}
	}
	return depthOf, nil
}

// Compact implements memory.Compactor.
func (p *Provider) Compact(ctx context.Context, session memory.Session, mode memory.CompactionMode) (*memory.CompactionResult, error) {
	if p.summarizer == nil {
		p.log.Debug("compaction disabled: no summarizer")
		return nil, nil
	}
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

func sqlTimeString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	default:
		return ""
	}
}

// parseTime parses a timestamp string into a UTC time.
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

// parseNullTime converts a pgtype.Timestamptz field into *time.Time, returning nil for
// NULL or the zero time.
func parseNullTime(ns pgtype.Timestamptz) *time.Time {
	if !ns.Valid || ns.Time.IsZero() {
		return nil
	}
	t := ns.Time.UTC()
	return &t
}
