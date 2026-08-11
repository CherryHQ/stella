package lcm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestBuildReviewContext_BudgetsSummariesBeforeMessages(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { db.Close() }()

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	convID := uuid.NewString()
	sess := memory.Session{ID: "review-budget-session", UserID: "user-1", AgentID: "agent-1", Channel: "test"}
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id) VALUES ($1, $2, 'test', 'chat', $3, $4)`, convID, sess.ID, sess.AgentID, sess.UserID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	large := strings.Repeat("s", 240_000)
	for i := 1; i <= 3; i++ {
		if _, err := db.Exec(ctx, `
			INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count, created_at)
			VALUES ($1, $2, $3, 0, $4, 60000, $5)
		`, fmt.Sprintf("summary-%d", i), convID, kindLeaf, fmt.Sprintf("summary %d %s", i, large), fmt.Sprintf("2026-01-01 00:00:0%d", i)); err != nil {
			t.Fatalf("insert summary %d: %v", i, err)
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count, created_at, actor_type)
		VALUES ($1, $2, 1, 'user', 'text', 'recent review message', 5, '2026-01-02 00:00:00', $3)
	`, uuid.NewString(), convID, eventlog.ActorHuman); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	got, err := p.BuildReviewContext(ctx, sess, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildReviewContext: %v", err)
	}
	if strings.Contains(got, `id="summary-1"`) || strings.Contains(got, `id="summary-2"`) {
		t.Fatalf("older summaries should be truncated by budget")
	}
	if !strings.Contains(got, `id="summary-3"`) {
		t.Fatalf("newest summary should be kept")
	}
	if !strings.Contains(got, "recent review message") {
		t.Fatalf("messages should use remaining budget, got length %d", len(got))
	}
}

func TestAppendReviewMessages_FiltersToolResults(t *testing.T) {
	msgs := []sqlc.CtxMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: roleTool, Content: strings.Repeat("x", 10000)},
		{Role: "user", Content: "thanks"},
	}

	var b strings.Builder
	appendReviewMessages(&b, msgs)
	got := b.String()

	if strings.Contains(got, "[tool]") {
		t.Error("tool result should be filtered out")
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "hi there") || !strings.Contains(got, "thanks") {
		t.Errorf("expected user/assistant messages preserved, got %q", got)
	}
}

func TestAppendReviewMessages_OnlyToolResults(t *testing.T) {
	msgs := []sqlc.CtxMessage{
		{Role: roleTool, Content: "result1"},
		{Role: roleTool, Content: "result2"},
	}

	var b strings.Builder
	appendReviewMessages(&b, msgs)

	if b.Len() != 0 {
		t.Errorf("expected empty output for tool-only messages, got %q", b.String())
	}
}

func TestAppendReviewMessages_TokenBudgetTruncation(t *testing.T) {
	// Each message is ~1000 tokens (4000 chars). With a 100K budget,
	// only the last ~100 should fit.
	content := strings.Repeat("a", 4000) // ~1000 tokens
	msgs := make([]sqlc.CtxMessage, 200)
	for i := range msgs {
		msgs[i] = sqlc.CtxMessage{Role: "user", Content: content}
	}

	var b strings.Builder
	appendReviewMessages(&b, msgs)
	got := b.String()

	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) >= 200 {
		t.Errorf("expected token budget to truncate messages, got %d lines", len(lines))
	}
	if len(lines) == 0 {
		t.Error("expected at least some messages in output")
	}
}

func TestAppendReviewMessages_KeepsTailNotHead(t *testing.T) {
	msgs := []sqlc.CtxMessage{
		{Role: "user", Content: strings.Repeat("old ", 25000)},
		{Role: "user", Content: "recent message"},
	}

	var b strings.Builder
	appendReviewMessages(&b, msgs)
	got := b.String()

	if !strings.Contains(got, "recent message") {
		t.Error("expected recent (tail) messages to be preserved")
	}
}

func TestAppendReviewMessages_Empty(t *testing.T) {
	var b strings.Builder
	appendReviewMessages(&b, nil)

	if b.Len() != 0 {
		t.Errorf("expected empty output for nil messages, got %q", b.String())
	}
}
