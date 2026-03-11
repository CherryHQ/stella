package lcm

import (
	"context"
	"database/sql"
	"testing"
)

func setupCompactionTest(t *testing.T) (*CompactionEngine, *Queries, *sql.DB, int64) {
	t.Helper()
	db, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, CreateConversationParams{SessionID: "sess-compact"})
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

	result := &CompactionResult{}
	if err := ce.leafPass(ctx, convID, result); err != nil {
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

	result := &CompactionResult{}
	if err := ce.leafPass(ctx, convID, result); err != nil {
		t.Fatalf("leafPass: %v", err)
	}

	if result.LeafSummariesCreated == 0 {
		t.Fatal("expected at least 1 leaf summary")
	}

	// Verify context items now contain a summary.
	items, err := q.GetContextItems(ctx, convID)
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
	ce, q, _, convID := setupCompactionTest(t)
	ctx := context.Background()

	// Manually create 3 leaf summaries in context.
	for i := 0; i < 3; i++ {
		sumID := generateSummaryID()
		err := q.CreateSummary(ctx, CreateSummaryParams{
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

	result := &CompactionResult{}
	if err := ce.condensedPass(ctx, convID, result); err != nil {
		t.Fatalf("condensedPass: %v", err)
	}

	if result.CondensedSummariesCreated == 0 {
		t.Fatal("expected condensed summary")
	}

	// Context should now have 1 condensed summary + 3 messages.
	items, err := q.GetContextItems(ctx, convID)
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
	items := []ContextItem{
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
	items := []ContextItem{
		{Ordinal: 0, ItemType: ItemTypeSummary},
		{Ordinal: 1, ItemType: ItemTypeSummary},
		{Ordinal: 2, ItemType: ItemTypeMessage},
		{Ordinal: 3, ItemType: ItemTypeSummary},
	}

	runs := findSummaryRuns(items, 2)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if len(runs[0].items) != 2 {
		t.Errorf("expected 2 items, got %d", len(runs[0].items))
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
