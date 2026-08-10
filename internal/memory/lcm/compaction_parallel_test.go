package lcm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type trackingSummarizer struct {
	delay  time.Duration
	failAt int64

	calls    atomic.Int64
	inFlight atomic.Int64
	maxSeen  atomic.Int64
}

func (s *trackingSummarizer) Summarize(ctx context.Context, text string, opts memory.SummarizeOptions) (string, error) {
	call := s.calls.Add(1)
	current := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		maxSeen := s.maxSeen.Load()
		if current <= maxSeen || s.maxSeen.CompareAndSwap(maxSeen, current) {
			break
		}
	}
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(s.delay):
		}
	}
	if s.failAt == call {
		return "", errors.New("boom")
	}
	return fmt.Sprintf("summary:%s", firstLine(text)), nil
}

func firstLine(text string) string {
	if before, _, ok := strings.Cut(text, "\n"); ok {
		return before
	}
	return text
}

func TestLeafPassSummarizesRunsConcurrently(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { db.Close() }()
	ctx := context.Background()
	q := sqlc.New(db)
	convID, items := seedLeafRuns(t, ctx, q, 4, defaultLeafChunkSize)

	summarizer := &trackingSummarizer{delay: 25 * time.Millisecond}
	engine := newCompactionEngine(db, q, summarizer, 0)
	result := &memory.CompactionResult{}
	if err := engine.leafPass(ctx, convID, items, result); err != nil {
		t.Fatalf("leafPass: %v", err)
	}
	if maxSeen := summarizer.maxSeen.Load(); maxSeen <= 1 || maxSeen > summarizeConcurrency {
		t.Fatalf("max in-flight = %d, want >1 and <=%d", maxSeen, summarizeConcurrency)
	}
	if result.LeafSummariesCreated != 4 || result.MessagesCompacted != 4*defaultLeafChunkSize {
		t.Fatalf("result = %+v", result)
	}

	summaries := listSummariesByOrdinal(t, ctx, q, convID)
	if len(summaries) != 8 {
		t.Fatalf("summary context items = %d, want 8 including separators", len(summaries))
	}
	for i := range 4 {
		got := summaries[i*2]
		want := fmt.Sprintf("summary:[user] run-%d-message-0", i)
		if got.content != want {
			t.Fatalf("summary %d content = %q, want %q", i, got.content, want)
		}
	}
}

func TestLeafPassSummarizeFailureWritesNothing(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { db.Close() }()
	ctx := context.Background()
	q := sqlc.New(db)
	convID, items := seedLeafRuns(t, ctx, q, 3, defaultLeafChunkSize)

	before := countSummaries(t, ctx, db, convID, kindLeaf)
	summarizer := &trackingSummarizer{delay: 10 * time.Millisecond, failAt: 2}
	engine := newCompactionEngine(db, q, summarizer, 0)
	result := &memory.CompactionResult{}
	if err := engine.leafPass(ctx, convID, items, result); err == nil {
		t.Fatal("leafPass succeeded, want summarize error")
	}
	if result.LeafSummariesCreated != 0 || result.MessagesCompacted != 0 {
		t.Fatalf("result mutated despite failed pass: %+v", result)
	}
	if count := countSummaries(t, ctx, db, convID, kindLeaf); count != before {
		t.Fatalf("leaf summaries written = %d, want unchanged %d", count, before)
	}
}

func TestLeafPassWritesSummariesInRunOrder(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { db.Close() }()
	ctx := context.Background()
	q := sqlc.New(db)
	convID, items := seedLeafRuns(t, ctx, q, 3, defaultLeafChunkSize)

	summarizer := &trackingSummarizer{delay: 5 * time.Millisecond}
	engine := newCompactionEngine(db, q, summarizer, 0)
	if err := engine.leafPass(ctx, convID, items, &memory.CompactionResult{}); err != nil {
		t.Fatalf("leafPass: %v", err)
	}

	summaries := listSummariesByOrdinal(t, ctx, q, convID)
	for i := range 3 {
		got := summaries[i*2]
		if got.ordinal != int64(i*(defaultLeafChunkSize+1)+1) {
			t.Fatalf("summary %d ordinal = %d", i, got.ordinal)
		}
		want := fmt.Sprintf("run-%d-message-0", i)
		if !strings.Contains(got.content, want) {
			t.Fatalf("summary %d content = %q, want %q", i, got.content, want)
		}
	}
}

func TestCondensedSummaryPropagatesMixedNonPrincipalDescendants(t *testing.T) {
	engine := &compactionEngine{summarizer: &trackingSummarizer{}}
	run := summaryRun{items: []sqlc.CtxItem{
		{SummaryID: pgtype.Text{String: "human-summary", Valid: true}},
		{SummaryID: pgtype.Text{String: "agent-summary", Valid: true}},
	}}
	got, err := engine.summarizeCondensedRun(t.Context(), run, map[string]sqlc.CtxSummary{
		"human-summary": {ID: "human-summary", Content: "human context"},
		"agent-summary": {ID: "agent-summary", Content: "agent context", ContainsNonPrincipalInput: true},
	})
	if err != nil {
		t.Fatalf("summarize mixed descendants: %v", err)
	}
	if !got.containsNonPrincipalInput {
		t.Fatal("condensed summary promoted mixed descendants by dropping non-principal provenance")
	}
}

func seedLeafRuns(t *testing.T, ctx context.Context, q *sqlc.Queries, runs, runSize int) (string, []sqlc.CtxItem) {
	t.Helper()
	convID := uuid.NewString()
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: convID, SessionID: convID, Channel: "test", Kind: "chat"}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	ordinal := int64(0)
	seq := int64(0)
	appendMessage := func(id, content string) {
		t.Helper()
		seq++
		ordinal++
		if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ID:             id,
			ConversationID: convID,
			Seq:            seq,
			Role:           roleUser,
			EventType:      eventTypeText,
			Content:        content,
			TokenCount:     int64(memory.EstimateTokens(content)),
			ActorType:      string(eventlog.ActorHuman),
		}); err != nil {
			t.Fatalf("create message: %v", err)
		}
		if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
			ConversationID: convID,
			Ordinal:        ordinal,
			ItemType:       itemTypeMessage,
			MessageID:      pgtype.Text{String: id, Valid: true},
			EventType:      eventTypeText,
			Role:           roleUser,
		}); err != nil {
			t.Fatalf("append message item: %v", err)
		}
	}
	for run := range runs {
		for i := range runSize {
			appendMessage(uuid.NewString(), fmt.Sprintf("run-%d-message-%d", run, i))
		}
		ordinal++
		separatorID := fmt.Sprintf("separator-%d", run)
		if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID:                      separatorID,
			ConversationID:          convID,
			Kind:                    kindLeaf,
			Depth:                   0,
			Content:                 "separator",
			TokenCount:              1,
			DescendantCount:         1,
			DescendantTokenCount:    1,
			SourceMessageTokenCount: 1,
		}); err != nil {
			t.Fatalf("create separator summary: %v", err)
		}
		if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
			ConversationID: convID,
			Ordinal:        ordinal,
			ItemType:       itemTypeSummary,
			SummaryID:      pgtype.Text{String: separatorID, Valid: true},
			Role:           "",
		}); err != nil {
			t.Fatalf("append separator item: %v", err)
		}
	}
	for i := range defaultFreshTail {
		appendMessage(uuid.NewString(), fmt.Sprintf("tail-message-%d", i))
	}
	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("get context items: %v", err)
	}
	return convID, items
}

type ordinalSummary struct {
	ordinal int64
	content string
}

func listSummariesByOrdinal(t *testing.T, ctx context.Context, q *sqlc.Queries, convID string) []ordinalSummary {
	t.Helper()
	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("get context items: %v", err)
	}
	out := make([]ordinalSummary, 0)
	for _, item := range items {
		if item.ItemType != itemTypeSummary || !item.SummaryID.Valid {
			continue
		}
		sum, err := q.GetSummary(ctx, sqlc.GetSummaryParams{ConversationID: convID, ID: item.SummaryID.String})
		if err != nil {
			t.Fatalf("get summary %s: %v", item.SummaryID.String, err)
		}
		out = append(out, ordinalSummary{ordinal: item.Ordinal, content: sum.Content})
	}
	return out
}

func countSummaries(t *testing.T, ctx context.Context, db *pgxpool.Pool, convID, kind string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_summary WHERE conversation_id = $1 AND kind = $2`, convID, kind).Scan(&count); err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	return count
}

var _ memory.Summarizer = (*trackingSummarizer)(nil)
