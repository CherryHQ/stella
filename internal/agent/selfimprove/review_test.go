package selfimprove

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
)

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	t.Run("Interval defaults to 1h", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{}
		if got := cfg.Interval(); got != "1h" {
			t.Errorf("Interval() = %q, want %q", got, "1h")
		}
	})

	t.Run("Interval uses configured value", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{Every: "30m"}
		if got := cfg.Interval(); got != "30m" {
			t.Errorf("Interval() = %q, want %q", got, "30m")
		}
	})

	t.Run("Batch defaults to 5", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{}
		if got := cfg.Batch(); got != 5 {
			t.Errorf("Batch() = %d, want %d", got, 5)
		}
	})

	t.Run("Batch uses configured value", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{BatchSize: 10}
		if got := cfg.Batch(); got != 10 {
			t.Errorf("Batch() = %d, want %d", got, 10)
		}
	})

	t.Run("Batch negative falls back to default", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{BatchSize: -1}
		if got := cfg.Batch(); got != 5 {
			t.Errorf("Batch() = %d, want %d", got, 5)
		}
	})

	t.Run("IsEnabled defaults to false", func(t *testing.T) {
		t.Parallel()
		cfg := config.SelfImproveConfig{}
		if cfg.IsEnabled() {
			t.Error("IsEnabled() = true, want false")
		}
	})

	t.Run("IsEnabled true when set", func(t *testing.T) {
		t.Parallel()
		enabled := true
		cfg := config.SelfImproveConfig{Enabled: &enabled}
		if !cfg.IsEnabled() {
			t.Error("IsEnabled() = false, want true")
		}
	})
}

func TestBuildConversationText_FirstReview(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	q := sqlc.New(db)
	ctx := context.Background()

	// Create a conversation.
	conv, err := q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
		SessionID:  "test-session-1",
		Channel:    "test",
		Archived:   0,
		LastActive: "2026-04-04T00:00:00Z",
		AgentID:    sql.NullString{String: "anna", Valid: true},
		UserID:     sql.NullInt64{Int64: 1, Valid: true},
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	// Insert messages.
	for i, msg := range []struct {
		role, content string
	}{
		{"user", "How do I deploy?"},
		{"assistant", "Run `kubectl apply -f deploy.yaml`."},
	} {
		_, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ConversationID: conv.ID,
			Seq:            int64(i + 1),
			Role:           msg.role,
			EventType:      "text",
			Content:        msg.content,
		})
		if err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}

	text, err := buildConversationText(ctx, q, conv)
	if err != nil {
		t.Fatalf("buildConversationText: %v", err)
	}

	if text == "" {
		t.Fatal("expected non-empty text")
	}
	if !contains(text, "[user] How do I deploy?") {
		t.Errorf("text missing user message: %s", text)
	}
	if !contains(text, "[assistant] Run") {
		t.Errorf("text missing assistant message: %s", text)
	}
	// Should not contain prior_context for first review.
	if contains(text, "<prior_context>") {
		t.Error("first review should not have prior_context")
	}
}

func TestBuildConversationText_Empty(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	q := sqlc.New(db)
	ctx := context.Background()

	conv, err := q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
		SessionID:  "test-session-empty",
		Channel:    "test",
		Archived:   0,
		LastActive: "2026-04-04T00:00:00Z",
		AgentID:    sql.NullString{String: "anna", Valid: true},
		UserID:     sql.NullInt64{Int64: 1, Valid: true},
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	text, err := buildConversationText(ctx, q, conv)
	if err != nil {
		t.Fatalf("buildConversationText: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
