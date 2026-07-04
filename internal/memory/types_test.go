package memory

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"hello world", 2}, // (11+3)/4 = 3
		{"a", 1},
	}
	for _, tc := range tests {
		got := EstimateTokens(tc.text)
		if got < 0 {
			t.Errorf("EstimateTokens(%q) returned negative: %d", tc.text, got)
		}
	}
	// At least verify non-empty > empty.
	if EstimateTokens("hello") <= EstimateTokens("") {
		t.Error("non-empty text should have more tokens than empty")
	}
}

func TestMessageText(t *testing.T) {
	tests := []struct {
		msg  ai.Message
		want string
	}{
		{ai.UserMessage{Content: "hello"}, "hello"},
		{ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "world"}}}, "world"},
		{ai.ToolResultMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "result"}}}, "result"},
	}
	for _, tc := range tests {
		got := MessageText(tc.msg)
		if got != tc.want {
			t.Errorf("MessageText(%T) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

func TestMessageRole(t *testing.T) {
	tests := []struct {
		msg  ai.Message
		want string
	}{
		{ai.UserMessage{}, "user"},
		{ai.AssistantMessage{}, "assistant"},
		{ai.ToolResultMessage{}, "tool"},
	}
	for _, tc := range tests {
		got := MessageRole(tc.msg)
		if got != tc.want {
			t.Errorf("MessageRole(%T) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

func TestMessageTimestamp(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tests := []struct {
		msg ai.Message
	}{
		{ai.UserMessage{Timestamp: now}},
		{ai.AssistantMessage{Timestamp: now}},
		{ai.ToolResultMessage{Timestamp: now}},
	}
	for _, tc := range tests {
		got := MessageTimestamp(tc.msg)
		if !got.Equal(now) {
			t.Errorf("MessageTimestamp(%T) = %v, want %v", tc.msg, got, now)
		}
	}
}

func TestContextValues(t *testing.T) {
	ctx := context.Background()

	ctx = WithSessionID(ctx, "sess-1")
	if got := SessionIDFromContext(ctx); got != "sess-1" {
		t.Errorf("expected 'sess-1', got %q", got)
	}

	ctx = authz.WithUserID(ctx, "42")
	if got := authz.UserIDFromContext(ctx); got != "42" {
		t.Errorf("expected '42', got %q", got)
	}

	ctx = authz.WithAgentID(ctx, "agent-1")
	if got := authz.AgentIDFromContext(ctx); got != "agent-1" {
		t.Errorf("expected 'agent-1', got %q", got)
	}
}

func TestContextValues_Missing(t *testing.T) {
	ctx := context.Background()
	if got := SessionIDFromContext(ctx); got != "" {
		t.Errorf("expected empty session ID, got %q", got)
	}
	if got := authz.UserIDFromContext(ctx); got != "" {
		t.Errorf("expected empty user ID, got %q", got)
	}
	if got := authz.AgentIDFromContext(ctx); got != "" {
		t.Errorf("expected empty agent ID, got %q", got)
	}
}

func TestCompactionModeString(t *testing.T) {
	if CompactionIncremental.String() != "incremental" {
		t.Errorf("unexpected string: %q", CompactionIncremental.String())
	}
	if CompactionFull.String() != "full" {
		t.Errorf("unexpected string: %q", CompactionFull.String())
	}
	unknown := CompactionMode(99)
	if unknown.String() != "unknown" {
		t.Errorf("unexpected string for unknown mode: %q", unknown.String())
	}
}
