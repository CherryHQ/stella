package memory

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vaayne/anna/internal/db/sqlc"
)

func setupCompactionTest(t *testing.T) (*CompactionEngine, *sqlc.Queries, *sql.DB, int64) {
	t.Helper()
	db, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-compact"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	summarizer := &StaticSummarizer{Response: "test summary"}
	ce := NewCompactionEngine(db, q, summarizer, 5)

	return ce, q, db, conv.ID
}

func TestLeafPass_NoCompaction(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	// Only 3 messages — below chunk size (10) and within fresh tail (5).
	for i := 1; i <= 3; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "short msg")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	result := &CompactionResult{}
	if err := ce.leafPass(ctx, convID, items, result); err != nil {
		t.Fatalf("leafPass: %v", err)
	}
	if result.LeafSummariesCreated != 0 {
		t.Errorf("expected no summaries, got %d", result.LeafSummariesCreated)
	}
}

func TestLeafPass_CreatesLeafSummary(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	// Override to smaller chunk size for testing.
	ce.freshTail = 2
	ctx := context.Background()

	// 15 messages, fresh tail = 2 → 13 eligible, chunk size = 10.
	for i := 1; i <= 15; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "message content")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	result := &CompactionResult{}
	if err := ce.leafPass(ctx, convID, items, result); err != nil {
		t.Fatalf("leafPass: %v", err)
	}

	if result.LeafSummariesCreated == 0 {
		t.Fatal("expected at least 1 leaf summary")
	}

	// Verify context items now contain a summary.
	items, err = q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}

	hasSummary := false
	for _, item := range items {
		if item.ItemType == ItemTypeSummary {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Error("expected summary in context items after leaf pass")
	}
}

func TestCondensedPass_CreateCondensedSummary(t *testing.T) {
	t.Run("same depth summaries are condensed", func(t *testing.T) {
		ce, q, _, convID := setupCompactionTest(t)
		ctx := context.Background()

		// Manually create 3 leaf summaries (all depth 0) in context.
		for i := 0; i < 3; i++ {
			sumID := generateSummaryID()
			err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
				ID: sumID, ConversationID: convID, Kind: KindLeaf,
				Depth: 0, Content: "leaf summary content", TokenCount: 5,
			})
			if err != nil {
				t.Fatalf("CreateSummary %d: %v", i, err)
			}
			addContextSummary(t, ctx, q, convID, i, sumID)
		}

		// Add some messages as fresh tail.
		for i := 1; i <= 3; i++ {
			msg := addMessage(t, ctx, q, convID, i, RoleUser, "recent message")
			addContextMessage(t, ctx, q, convID, i+2, msg.ID) // ordinals 3,4,5
		}

		items, err := q.GetContextItems(ctx, convID)
		if err != nil {
			t.Fatalf("GetContextItems: %v", err)
		}
		result := &CompactionResult{}
		if err := ce.condensedPass(ctx, convID, items, result); err != nil {
			t.Fatalf("condensedPass: %v", err)
		}

		if result.CondensedSummariesCreated == 0 {
			t.Fatal("expected condensed summary")
		}

		// Context should now have 1 condensed summary + 3 messages.
		items, err = q.GetContextItems(ctx, convID)
		if err != nil {
			t.Fatalf("GetContextItems: %v", err)
		}

		summaryCount := 0
		messageCount := 0
		for _, item := range items {
			if item.ItemType == ItemTypeSummary {
				summaryCount++
			} else {
				messageCount++
			}
		}
		if summaryCount != 1 {
			t.Errorf("expected 1 summary, got %d", summaryCount)
		}
		if messageCount != 3 {
			t.Errorf("expected 3 messages, got %d", messageCount)
		}
	})

	t.Run("mixed depth summaries are not grouped", func(t *testing.T) {
		ce, q, _, convID := setupCompactionTest(t)
		ctx := context.Background()

		// Create one depth-0 leaf summary.
		sum0 := generateSummaryID()
		err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID: sum0, ConversationID: convID, Kind: KindLeaf,
			Depth: 0, Content: "leaf summary", TokenCount: 5,
		})
		if err != nil {
			t.Fatalf("CreateSummary depth-0: %v", err)
		}
		addContextSummary(t, ctx, q, convID, 0, sum0)

		// Create one depth-1 condensed summary.
		sum1 := generateSummaryID()
		err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID: sum1, ConversationID: convID, Kind: KindCondensed,
			Depth: 1, Content: "condensed summary", TokenCount: 5,
		})
		if err != nil {
			t.Fatalf("CreateSummary depth-1: %v", err)
		}
		addContextSummary(t, ctx, q, convID, 1, sum1)

		items, err := q.GetContextItems(ctx, convID)
		if err != nil {
			t.Fatalf("GetContextItems: %v", err)
		}
		result := &CompactionResult{}
		if err := ce.condensedPass(ctx, convID, items, result); err != nil {
			t.Fatalf("condensedPass: %v", err)
		}

		// Different depths should NOT be grouped, so no condensation.
		if result.CondensedSummariesCreated != 0 {
			t.Errorf("expected no condensation for mixed depths, got %d", result.CondensedSummariesCreated)
		}
	})
}

func TestCompact_Incremental(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ce.freshTail = 2
	ctx := context.Background()

	for i := 1; i <= 15; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "message content here")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	result, err := ce.Compact(ctx, convID, CompactionIncremental)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.LeafSummariesCreated == 0 {
		t.Error("expected leaf summaries from incremental compaction")
	}
	if result.TokensBefore == 0 {
		t.Error("expected non-zero tokens before")
	}
}

func TestCompact_Full(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ce.freshTail = 2
	ctx := context.Background()

	// Create enough messages for multiple compaction passes.
	for i := 1; i <= 30; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "message content here")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	result, err := ce.Compact(ctx, convID, CompactionFull)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.LeafSummariesCreated == 0 {
		t.Error("expected leaf summaries from full compaction")
	}
}

func TestFindMessageRuns(t *testing.T) {
	items := []sqlc.CtxItem{
		{Ordinal: 0, ItemType: ItemTypeMessage},
		{Ordinal: 1, ItemType: ItemTypeMessage},
		{Ordinal: 2, ItemType: ItemTypeSummary},
		{Ordinal: 3, ItemType: ItemTypeMessage},
		{Ordinal: 4, ItemType: ItemTypeMessage},
		{Ordinal: 5, ItemType: ItemTypeMessage},
	}

	runs := findMessageRuns(items, 2)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if len(runs[0].items) != 2 {
		t.Errorf("run 0: expected 2 items, got %d", len(runs[0].items))
	}
	if len(runs[1].items) != 3 {
		t.Errorf("run 1: expected 3 items, got %d", len(runs[1].items))
	}
}

func TestFindSummaryRuns(t *testing.T) {
	t.Run("basic grouping", func(t *testing.T) {
		items := []sqlc.CtxItem{
			{Ordinal: 0, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s1", Valid: true}},
			{Ordinal: 1, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s2", Valid: true}},
			{Ordinal: 2, ItemType: ItemTypeMessage},
			{Ordinal: 3, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s3", Valid: true}},
		}
		depthOf := map[string]int64{"s1": 0, "s2": 0, "s3": 0}

		runs := findSummaryRuns(items, 2, depthOf)
		if len(runs) != 1 {
			t.Fatalf("expected 1 run, got %d", len(runs))
		}
		if len(runs[0].items) != 2 {
			t.Errorf("expected 2 items, got %d", len(runs[0].items))
		}
	})

	t.Run("different depths break runs", func(t *testing.T) {
		items := []sqlc.CtxItem{
			{Ordinal: 0, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s1", Valid: true}},
			{Ordinal: 1, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s2", Valid: true}},
			{Ordinal: 2, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s3", Valid: true}},
		}
		depthOf := map[string]int64{"s1": 0, "s2": 0, "s3": 1}

		runs := findSummaryRuns(items, 2, depthOf)
		if len(runs) != 1 {
			t.Fatalf("expected 1 run (depth-0 pair only), got %d", len(runs))
		}
		if len(runs[0].items) != 2 {
			t.Errorf("expected 2 items in run, got %d", len(runs[0].items))
		}
	})

	t.Run("alternating depths produce no runs", func(t *testing.T) {
		items := []sqlc.CtxItem{
			{Ordinal: 0, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s1", Valid: true}},
			{Ordinal: 1, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s2", Valid: true}},
			{Ordinal: 2, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s3", Valid: true}},
		}
		depthOf := map[string]int64{"s1": 0, "s2": 1, "s3": 0}

		runs := findSummaryRuns(items, 2, depthOf)
		if len(runs) != 0 {
			t.Fatalf("expected 0 runs with alternating depths, got %d", len(runs))
		}
	})

	t.Run("multiple same-depth runs", func(t *testing.T) {
		items := []sqlc.CtxItem{
			{Ordinal: 0, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s1", Valid: true}},
			{Ordinal: 1, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s2", Valid: true}},
			{Ordinal: 2, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s3", Valid: true}},
			{Ordinal: 3, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s4", Valid: true}},
			{Ordinal: 4, ItemType: ItemTypeSummary, SummaryID: sql.NullString{String: "s5", Valid: true}},
		}
		depthOf := map[string]int64{"s1": 0, "s2": 0, "s3": 1, "s4": 1, "s5": 1}

		runs := findSummaryRuns(items, 2, depthOf)
		if len(runs) != 2 {
			t.Fatalf("expected 2 runs, got %d", len(runs))
		}
		if len(runs[0].items) != 2 {
			t.Errorf("run 0: expected 2 items, got %d", len(runs[0].items))
		}
		if len(runs[1].items) != 3 {
			t.Errorf("run 1: expected 3 items, got %d", len(runs[1].items))
		}
	})
}

func TestLeafPass_EmptyContext(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	result := &CompactionResult{}
	if err := ce.leafPass(ctx, convID, items, result); err != nil {
		t.Fatalf("leafPass: %v", err)
	}
	if result.LeafSummariesCreated != 0 {
		t.Errorf("expected 0 summaries, got %d", result.LeafSummariesCreated)
	}
}

func TestLeafPass_AllWithinFreshTail(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "recent message content here")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	result := &CompactionResult{}
	if err := ce.leafPass(ctx, convID, items, result); err != nil {
		t.Fatalf("leafPass: %v", err)
	}
	if result.LeafSummariesCreated != 0 {
		t.Errorf("expected 0 summaries when all in fresh tail, got %d", result.LeafSummariesCreated)
	}
}

func TestCondensedPass_NoSummaryItems(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "message")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	result := &CompactionResult{}
	if err := ce.condensedPass(ctx, convID, items, result); err != nil {
		t.Fatalf("condensedPass: %v", err)
	}
	if result.CondensedSummariesCreated != 0 {
		t.Errorf("expected 0 condensed summaries, got %d", result.CondensedSummariesCreated)
	}
}

func TestCompact_FullSafetyLimit(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ce.freshTail = 0

	ce.summarizer = &LLMSummarizer{
		Generate: func(_ context.Context, _ string) (string, error) {
			return "short", nil
		},
	}
	ctx := context.Background()

	for i := 1; i <= 200; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "message content for safety limit test")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	result, err := ce.Compact(ctx, convID, CompactionFull)
	if err != nil {
		t.Fatalf("Compact full: %v", err)
	}

	if result.LeafSummariesCreated == 0 {
		t.Error("expected leaf summaries from full compaction with safety limit")
	}
	if result.Duration == 0 {
		t.Error("expected non-zero duration")
	}
}

func TestCompactMessageRun_InvalidMessageID(t *testing.T) {
	ce, _, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	run := messageRun{
		items: []sqlc.CtxItem{
			{Ordinal: 0, ItemType: ItemTypeMessage, MessageID: sql.NullInt64{Valid: false}},
			{Ordinal: 1, ItemType: ItemTypeMessage, MessageID: sql.NullInt64{Valid: false}},
		},
		startOrd: 0,
		endOrd:   1,
	}

	result := &CompactionResult{}
	err := ce.compactMessageRun(ctx, convID, run, result)
	if err != nil {
		t.Fatalf("compactMessageRun: %v", err)
	}

	if result.LeafSummariesCreated != 0 {
		t.Errorf("expected 0 summaries for invalid message IDs, got %d", result.LeafSummariesCreated)
	}
	if result.MessagesCompacted != 0 {
		t.Errorf("expected 0 messages compacted, got %d", result.MessagesCompacted)
	}
}

func TestNewCompactionEngine_DefaultFreshTail(t *testing.T) {
	db, q := testDB(t)
	summarizer := &StaticSummarizer{Response: "test"}

	ce := NewCompactionEngine(db, q, summarizer, 0)
	if ce.freshTail != DefaultFreshTail {
		t.Errorf("expected freshTail=%d, got %d", DefaultFreshTail, ce.freshTail)
	}

	ce2 := NewCompactionEngine(db, q, summarizer, -5)
	if ce2.freshTail != DefaultFreshTail {
		t.Errorf("expected freshTail=%d for negative input, got %d", DefaultFreshTail, ce2.freshTail)
	}
}

func TestCondensedPass_WithTimestamps(t *testing.T) {
	ce, q, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		sumID := generateSummaryID()
		err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID:                   sumID,
			ConversationID:       convID,
			Kind:                 KindLeaf,
			Depth:                0,
			Content:              "leaf summary with timestamps",
			TokenCount:           10,
			EarliestAt:           sql.NullString{String: "2026-01-01T00:00:00Z", Valid: true},
			LatestAt:             sql.NullString{String: "2026-01-02T00:00:00Z", Valid: true},
			DescendantCount:      3,
			DescendantTokenCount: 30,
		})
		if err != nil {
			t.Fatalf("CreateSummary %d: %v", i, err)
		}
		addContextSummary(t, ctx, q, convID, i, sumID)
	}

	items, err := q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	result := &CompactionResult{}
	if err := ce.condensedPass(ctx, convID, items, result); err != nil {
		t.Fatalf("condensedPass: %v", err)
	}
	if result.CondensedSummariesCreated != 1 {
		t.Errorf("expected 1 condensed summary, got %d", result.CondensedSummariesCreated)
	}
}

func TestCompact_IncrementalEmpty(t *testing.T) {
	ce, _, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	result, err := ce.Compact(ctx, convID, CompactionIncremental)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.LeafSummariesCreated != 0 {
		t.Errorf("expected 0 leaf summaries, got %d", result.LeafSummariesCreated)
	}
	if result.CondensedSummariesCreated != 0 {
		t.Errorf("expected 0 condensed summaries, got %d", result.CondensedSummariesCreated)
	}
	if result.TokensBefore != 0 {
		t.Errorf("expected 0 tokens before, got %d", result.TokensBefore)
	}
}

func TestCompact_FullEmpty(t *testing.T) {
	ce, _, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	result, err := ce.Compact(ctx, convID, CompactionFull)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.LeafSummariesCreated != 0 {
		t.Errorf("expected 0 leaf summaries, got %d", result.LeafSummariesCreated)
	}
}

func TestGenerateSummaryID(t *testing.T) {
	id1 := generateSummaryID()
	id2 := generateSummaryID()

	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	if len(id1) != 4+16 { // "sum_" + 16 hex chars
		t.Errorf("expected 20 chars, got %d: %s", len(id1), id1)
	}
}

func TestQueryCoverage_DeleteAllContextItems(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-del-all"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg := addMessage(t, ctx, q, conv.ID, 1, RoleUser, "msg")
	addContextMessage(t, ctx, q, conv.ID, 0, msg.ID)

	if err := q.DeleteAllContextItems(ctx, conv.ID); err != nil {
		t.Fatalf("DeleteAllContextItems: %v", err)
	}

	items, err := q.GetContextItems(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items after delete all, got %d", len(items))
	}
}

func TestQueryCoverage_GetContextItemCount(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-count"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	count, err := q.GetContextItemCount(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextItemCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	msg := addMessage(t, ctx, q, conv.ID, 1, RoleUser, "msg")
	addContextMessage(t, ctx, q, conv.ID, 0, msg.ID)

	count, err = q.GetContextItemCount(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextItemCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}
}

func TestQueryCoverage_GetContextMessageItems(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-msg-items"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg := addMessage(t, ctx, q, conv.ID, 1, RoleUser, "hello")
	addContextMessage(t, ctx, q, conv.ID, 0, msg.ID)

	rows, err := q.GetContextMessageItems(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetContextMessageItems: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].MsgTokenCount != msg.TokenCount {
		t.Errorf("token count = %d, want %d", rows[0].MsgTokenCount, msg.TokenCount)
	}
}

func TestQueryCoverage_GetFreshTailMessageIDs(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-tail"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	for i := 1; i <= 5; i++ {
		msg := addMessage(t, ctx, q, conv.ID, i, RoleUser, "msg")
		addContextMessage(t, ctx, q, conv.ID, i-1, msg.ID)
	}

	ids, err := q.GetFreshTailMessageIDs(ctx, sqlc.GetFreshTailMessageIDsParams{
		ConversationID: conv.ID,
		Limit:          3,
	})
	if err != nil {
		t.Fatalf("GetFreshTailMessageIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 tail IDs, got %d", len(ids))
	}
}

func TestQueryCoverage_GetMaxContextOrdinal(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-max-ord"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	maxOrd, err := q.GetMaxContextOrdinal(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetMaxContextOrdinal: %v", err)
	}
	if maxOrd != 0 {
		t.Errorf("expected 0 for empty, got %d", maxOrd)
	}

	msg := addMessage(t, ctx, q, conv.ID, 1, RoleUser, "msg")
	addContextMessage(t, ctx, q, conv.ID, 5, msg.ID)

	maxOrd, err = q.GetMaxContextOrdinal(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetMaxContextOrdinal: %v", err)
	}
	if maxOrd != 5 {
		t.Errorf("expected 5, got %d", maxOrd)
	}
}

func TestQueryCoverage_GetMessagePartsByMessages(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-parts"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	msg1 := addMessage(t, ctx, q, conv.ID, 1, RoleUser, "msg1")
	msg2 := addMessage(t, ctx, q, conv.ID, 2, RoleAssistant, "msg2")

	err = q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID: "p1", MessageID: msg1.ID, PartType: "text", Ordinal: 0,
		TextContent: sql.NullString{String: "part1", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateMessagePart: %v", err)
	}
	err = q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{
		ID: "p2", MessageID: msg2.ID, PartType: "text", Ordinal: 0,
		TextContent: sql.NullString{String: "part2", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateMessagePart: %v", err)
	}

	parts, err := q.GetMessagePartsByMessages(ctx, []int64{msg1.ID, msg2.ID})
	if err != nil {
		t.Fatalf("GetMessagePartsByMessages: %v", err)
	}
	if len(parts) != 2 {
		t.Errorf("expected 2 parts, got %d", len(parts))
	}

	parts, err = q.GetMessagePartsByMessages(ctx, []int64{})
	if err != nil {
		t.Fatalf("GetMessagePartsByMessages empty: %v", err)
	}
	if len(parts) != 0 {
		t.Errorf("expected 0 parts for empty input, got %d", len(parts))
	}
}

func TestQueryCoverage_GetMessagesByConversationRange(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-range"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	for i := 1; i <= 5; i++ {
		addMessage(t, ctx, q, conv.ID, i, RoleUser, "msg")
	}

	msgs, err := q.GetMessagesByConversationRange(ctx, sqlc.GetMessagesByConversationRangeParams{
		ConversationID: conv.ID,
		Seq:            2,
		Seq_2:          4,
	})
	if err != nil {
		t.Fatalf("GetMessagesByConversationRange: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages in range [2,4], got %d", len(msgs))
	}
}

func TestQueryCoverage_GetSummariesByConversation(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-sums"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	for i := 0; i < 3; i++ {
		err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID: generateSummaryID(), ConversationID: conv.ID,
			Kind: KindLeaf, Depth: 0, Content: "summary", TokenCount: 5,
		})
		if err != nil {
			t.Fatalf("CreateSummary: %v", err)
		}
	}

	sums, err := q.GetSummariesByConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetSummariesByConversation: %v", err)
	}
	if len(sums) != 3 {
		t.Errorf("expected 3 summaries, got %d", len(sums))
	}
}

func TestQueryCoverage_GetSummariesByDepth(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-depth"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: generateSummaryID(), ConversationID: conv.ID,
		Kind: KindLeaf, Depth: 0, Content: "leaf", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}
	err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: generateSummaryID(), ConversationID: conv.ID,
		Kind: KindCondensed, Depth: 1, Content: "condensed", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	sums, err := q.GetSummariesByDepth(ctx, sqlc.GetSummariesByDepthParams{
		ConversationID: conv.ID, Depth: 0,
	})
	if err != nil {
		t.Fatalf("GetSummariesByDepth: %v", err)
	}
	if len(sums) != 1 {
		t.Errorf("expected 1 depth-0 summary, got %d", len(sums))
	}
}

func TestQueryCoverage_SearchSummaries(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-search-sum"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	err = q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: generateSummaryID(), ConversationID: conv.ID,
		Kind: KindLeaf, Depth: 0, Content: "implemented authentication flow", TokenCount: 5,
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	results, err := q.SearchSummaries(ctx, sqlc.SearchSummariesParams{
		ConversationID: conv.ID, Content: "%auth%", Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchSummaries: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestQueryCoverage_UpdateConversationBootstrapped(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-bootstrap"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	if err := q.UpdateConversationBootstrapped(ctx, conv.ID); err != nil {
		t.Fatalf("UpdateConversationBootstrapped: %v", err)
	}

	got, err := q.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if !got.BootstrappedAt.Valid {
		t.Error("expected bootstrapped_at to be set")
	}
}
