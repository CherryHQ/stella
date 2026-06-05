package lcm

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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
