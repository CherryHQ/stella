package lcm_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestReconstructedProviderReusesCommittedCompaction(t *testing.T) {
	ctx := context.Background()
	db := newLCMTestDB(t)
	defer db.Close()
	session := newLCMTestSession("reconstructed-compaction")
	var calls atomic.Int64
	summarizer := func(context.Context, string) (string, error) {
		calls.Add(1)
		return "committed compaction summary", nil
	}
	config := map[string]any{"fresh_tail": 1}

	first, err := lcm.New(db, summarizer, config)
	if err != nil {
		t.Fatalf("new first provider: %v", err)
	}
	if err := first.Bootstrap(ctx, session); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for i := 1; i <= 11; i++ {
		if err := first.Append(ctx, session, ai.UserMessage{Content: fmt.Sprintf("private message %02d", i)}); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
	firstResult, err := memory.Compactor(first).Compact(ctx, session, memory.CompactionIncremental)
	if err != nil {
		t.Fatalf("first compact: %v", err)
	}
	if firstResult.LeafSummariesCreated == 0 || firstResult.MessagesCompacted == 0 {
		t.Fatalf("first compact result = %+v, want committed leaf compaction", firstResult)
	}
	if calls.Load() == 0 {
		t.Fatal("first compact did not call the summarizer")
	}
	callsAfterFirst := calls.Load()

	convID := conversationID(t, db, session.ID)
	var summaryCount, itemCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_summary WHERE conversation_id = $1`, convID).Scan(&summaryCount); err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_item WHERE conversation_id = $1`, convID).Scan(&itemCount); err != nil {
		t.Fatalf("count context items: %v", err)
	}
	if summaryCount == 0 || itemCount == 0 {
		t.Fatalf("committed rows: ctx_summary=%d ctx_item=%d, want persisted compaction", summaryCount, itemCount)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first provider: %v", err)
	}

	second, err := lcm.New(db, summarizer, config)
	if err != nil {
		t.Fatalf("new reconstructed provider: %v", err)
	}
	defer func() { _ = second.Close() }()
	assembled, err := second.Assemble(ctx, session, 100_000, 1)
	if err != nil {
		t.Fatalf("assemble committed compaction: %v", err)
	}
	assembledText := make([]string, 0, len(assembled))
	for _, message := range assembled {
		assembledText = append(assembledText, memory.MessageText(message))
	}
	assembledContext := strings.Join(assembledText, "\n")
	if !strings.Contains(assembledContext, "committed compaction summary") || !strings.Contains(assembledContext, "private message 11") {
		t.Fatalf("assembled committed compaction = %#v", assembled)
	}

	secondResult, err := memory.Compactor(second).Compact(ctx, session, memory.CompactionIncremental)
	if err != nil {
		t.Fatalf("second compact: %v", err)
	}
	if secondResult.LeafSummariesCreated != 0 || secondResult.CondensedSummariesCreated != 0 || secondResult.MessagesCompacted != 0 {
		t.Fatalf("second compact result = %+v, want no new work", secondResult)
	}
	if calls.Load() != callsAfterFirst {
		t.Fatalf("second compact summarizer calls = %d, want unchanged %d", calls.Load(), callsAfterFirst)
	}
	var finalSummaryCount, finalItemCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_summary WHERE conversation_id = $1`, convID).Scan(&finalSummaryCount); err != nil {
		t.Fatalf("count summaries after reconstruction: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_item WHERE conversation_id = $1`, convID).Scan(&finalItemCount); err != nil {
		t.Fatalf("count context items after reconstruction: %v", err)
	}
	if finalSummaryCount != summaryCount || finalItemCount != itemCount {
		t.Fatalf("reconstructed compact wrote rows: ctx_summary=%d ctx_item=%d, want unchanged %d/%d", finalSummaryCount, finalItemCount, summaryCount, itemCount)
	}
}
