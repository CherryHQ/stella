package memory

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vaayne/anna/db/sqlc"
)

func setupAssemblerTest(t *testing.T) (*Assembler, *sqlc.Queries, int64) {
	t.Helper()
	_, q := testDB(t)
	ctx := context.Background()

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{SessionID: "sess-test"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	return NewAssembler(q), q, conv.ID
}

func addMessage(t *testing.T, ctx context.Context, q *sqlc.Queries, convID int64, seq int, role, content string) sqlc.Message {
	t.Helper()
	msg, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ConversationID: convID,
		Seq:            int64(seq),
		Role:           role,
		Content:        content,
		TokenCount:     int64(EstimateTokens(content)),
	})
	if err != nil {
		t.Fatalf("CreateMessage seq=%d: %v", seq, err)
	}
	return msg
}

func addContextMessage(t *testing.T, ctx context.Context, q *sqlc.Queries, convID int64, ordinal int, msgID int64) {
	t.Helper()
	err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        int64(ordinal),
		ItemType:       ItemTypeMessage,
		MessageID:      sql.NullInt64{Int64: msgID, Valid: true},
	})
	if err != nil {
		t.Fatalf("AppendContextItem ordinal=%d: %v", ordinal, err)
	}
}

func addContextSummary(t *testing.T, ctx context.Context, q *sqlc.Queries, convID int64, ordinal int, sumID string) {
	t.Helper()
	err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        int64(ordinal),
		ItemType:       ItemTypeSummary,
		SummaryID:      sql.NullString{String: sumID, Valid: true},
	})
	if err != nil {
		t.Fatalf("AppendContextItem summary ordinal=%d: %v", ordinal, err)
	}
}

func TestAssemble_EmptyContext(t *testing.T) {
	asm, _, convID := setupAssemblerTest(t)
	ctx := context.Background()

	result, err := asm.Assemble(ctx, convID, 10000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d events", len(result))
	}
}

func TestAssemble_AllMessagesFit(t *testing.T) {
	asm, q, convID := setupAssemblerTest(t)
	ctx := context.Background()

	msg1 := addMessage(t, ctx, q, convID, 1, RoleUser, "hello")
	msg2 := addMessage(t, ctx, q, convID, 2, RoleAssistant, "hi there")

	addContextMessage(t, ctx, q, convID, 0, msg1.ID)
	addContextMessage(t, ctx, q, convID, 1, msg2.ID)

	result, err := asm.Assemble(ctx, convID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result))
	}
}

func TestAssemble_BudgetExclusion(t *testing.T) {
	asm, q, convID := setupAssemblerTest(t)
	ctx := context.Background()

	// Create 5 messages with known content.
	for i := 1; i <= 5; i++ {
		msg := addMessage(t, ctx, q, convID, i, RoleUser, "message content here")
		addContextMessage(t, ctx, q, convID, i-1, msg.ID)
	}

	// Budget that can only fit ~2 messages (each ~5 tokens = 20 chars / 4).
	// Fresh tail = 1, so the last message is always included.
	result, err := asm.Assemble(ctx, convID, 12, 1)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Should include fresh tail (1 message) + as many older messages as fit.
	if len(result) < 1 {
		t.Fatalf("expected at least 1 event (fresh tail), got %d", len(result))
	}
	if len(result) > 3 {
		t.Errorf("expected budget to exclude some messages, got %d events", len(result))
	}
}

func TestAssemble_FreshTailProtected(t *testing.T) {
	asm, q, convID := setupAssemblerTest(t)
	ctx := context.Background()

	// 3 messages, fresh tail = 2.
	msg1 := addMessage(t, ctx, q, convID, 1, RoleUser, "old message")
	msg2 := addMessage(t, ctx, q, convID, 2, RoleUser, "recent one")
	msg3 := addMessage(t, ctx, q, convID, 3, RoleAssistant, "response")

	addContextMessage(t, ctx, q, convID, 0, msg1.ID)
	addContextMessage(t, ctx, q, convID, 1, msg2.ID)
	addContextMessage(t, ctx, q, convID, 2, msg3.ID)

	// Tiny budget — only fits fresh tail.
	result, err := asm.Assemble(ctx, convID, 8, 2)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Fresh tail of 2 messages should always be included.
	if len(result) < 2 {
		t.Errorf("expected at least 2 events (fresh tail), got %d", len(result))
	}
}

func TestAssemble_WithSummary(t *testing.T) {
	asm, q, convID := setupAssemblerTest(t)
	ctx := context.Background()

	// Add a summary + recent message.
	err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
		ID: "sum_test01", ConversationID: convID, Kind: KindLeaf,
		Depth: 0, Content: "Earlier conversation about auth", TokenCount: 8,
		EarliestAt: sql.NullString{String: "2026-03-10T10:00:00", Valid: true},
		LatestAt:   sql.NullString{String: "2026-03-10T11:00:00", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateSummary: %v", err)
	}

	msg := addMessage(t, ctx, q, convID, 1, RoleUser, "what about auth?")
	addContextSummary(t, ctx, q, convID, 0, "sum_test01")
	addContextMessage(t, ctx, q, convID, 1, msg.ID)

	result, err := asm.Assemble(ctx, convID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result))
	}

	// First event should be the summary (as a user message with XML).
	if result[0].Type != "user_message" {
		t.Errorf("expected user_message type for summary, got %q", result[0].Type)
	}
	if result[0].Summary == "" {
		t.Error("expected summary content in first event")
	}
}

func TestFormatSummaryXML_Leaf(t *testing.T) {
	sum := sqlc.Summary{
		ID: "sum_abc123", Kind: KindLeaf, Depth: 0,
		Content:    "Discussed authentication flow",
		EarliestAt: sql.NullString{String: "2026-03-10T14:30:00", Valid: true},
		LatestAt:   sql.NullString{String: "2026-03-10T15:45:00", Valid: true},
	}
	xml := FormatSummaryXML(sum, nil)

	if !containsAll(xml, `id="sum_abc123"`, `kind="leaf"`, `depth="0"`, `earliest_at=`, `latest_at=`, "<content>", "</summary>") {
		t.Errorf("XML missing expected attributes:\n%s", xml)
	}
	if contains(xml, "<parents>") {
		t.Error("leaf summary should not have parents section")
	}
}

func TestFormatSummaryXML_Condensed(t *testing.T) {
	sum := sqlc.Summary{
		ID: "sum_xyz789", Kind: KindCondensed, Depth: 1,
		Content:         "High-level auth + database work",
		DescendantCount: 4,
	}
	parents := []sqlc.Summary{
		{ID: "sum_abc123"},
		{ID: "sum_def456"},
	}
	xml := FormatSummaryXML(sum, parents)

	if !containsAll(xml, `kind="condensed"`, `depth="1"`, `descendant_count="4"`, "<parents>", `<summary_ref id="sum_abc123"`, `<summary_ref id="sum_def456"`) {
		t.Errorf("condensed XML missing expected content:\n%s", xml)
	}
}

func TestSplitFreshTail(t *testing.T) {
	items := []sqlc.ContextItem{
		{Ordinal: 0, ItemType: ItemTypeSummary},
		{Ordinal: 1, ItemType: ItemTypeMessage},
		{Ordinal: 2, ItemType: ItemTypeMessage},
		{Ordinal: 3, ItemType: ItemTypeMessage},
	}

	tail, older := splitFreshTail(items, 2)
	// Fresh tail starts at the 2nd message from end (ordinal 2).
	// tail = [msg@2, msg@3], older = [summary@0, msg@1]
	if len(tail) != 2 {
		t.Errorf("tail len = %d, want 2", len(tail))
	}
	if len(older) != 2 {
		t.Errorf("older len = %d, want 2", len(older))
	}
}

// helpers

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !containsStr(s, sub) {
			return false
		}
	}
	return true
}
