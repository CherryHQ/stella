package lcm_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/plugins/memory/lcm"
)

func newLCMTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	_, err = db.Exec(`INSERT INTO settings_agents (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled)
		VALUES ('test', 'Test Agent', '', '', '', '', '', 'system', 0, 1)`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed agent: %v", err)
	}
	return db
}

func newLCMTestProvider(t *testing.T) (memory.Provider, func()) {
	t.Helper()
	db := newLCMTestDB(t)

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("new provider: %v", err)
	}
	return p, func() {
		_ = p.Close()
		_ = db.Close()
	}
}

func newLCMTestSession(suffix string) memory.Session {
	return memory.Session{
		ID:      "test:cli:1:" + suffix,
		AgentID: "test",
		UserID:  1,
		Channel: "cli",
	}
}

func TestLCMProvider_CompactSummarizerCanReadDB(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { _ = db.Close() }()

	p, err := lcm.New(db, func(ctx context.Context, _ string) (string, error) {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings_agents`).Scan(&count); err != nil {
			return "", err
		}
		return "summary from db-backed summarizer", nil
	}, map[string]any{"fresh_tail": 1})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sess := newLCMTestSession("compact-db-summarizer")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("message %02d with enough content", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	result, err := p.Compact(ctx, sess, memory.CompactionFull)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if result.LeafSummariesCreated == 0 {
		t.Fatal("expected at least one leaf summary")
	}

	var content string
	if err := db.QueryRowContext(ctx, `SELECT content FROM ctx_summaries ORDER BY created_at DESC LIMIT 1`).Scan(&content); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if content != "summary from db-backed summarizer" {
		t.Fatalf("summary content = %q", content)
	}
}

func TestLCMProvider_AppendAssistantMessage(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	ctx := context.Background()
	sess := newLCMTestSession("assist")

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	msgs := []ai.Message{
		ai.UserMessage{Content: "what is 2+2?", Timestamp: now},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "4"}},
			StopReason: ai.StopReasonStop,
			Timestamp:  now.Add(time.Second),
		},
	}

	if err := p.Append(ctx, sess, msgs...); err != nil {
		t.Fatal(err)
	}

	got, err := p.Assemble(ctx, sess, 100000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Errorf("expected at least 2 messages, got %d", len(got))
	}
}

func TestLCMProvider_AppendToolMessages(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	ctx := context.Background()
	sess := newLCMTestSession("tools")

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	msgs := []ai.Message{
		ai.UserMessage{Content: "list files", Timestamp: now},
		ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "tc1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
			},
			StopReason: ai.StopReasonToolUse,
			Timestamp:  now.Add(time.Second),
		},
		ai.ToolResultMessage{
			ToolCallID: "tc1",
			Content:    []ai.ContentBlock{ai.TextContent{Text: "file.txt\ndir/"}},
			Timestamp:  now.Add(2 * time.Second),
		},
	}

	if err := p.Append(ctx, sess, msgs...); err != nil {
		t.Fatal(err)
	}

	got, err := p.Assemble(ctx, sess, 100000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Errorf("expected at least 2 messages, got %d", len(got))
	}
}

func TestLCMProvider_LoadHistory(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	ctx := context.Background()
	sess := newLCMTestSession("history")

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	msgs := []ai.Message{
		ai.UserMessage{Content: "hello", Timestamp: now},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "hi"}},
			StopReason: ai.StopReasonStop,
			Timestamp:  now.Add(time.Second),
		},
	}

	if err := p.Append(ctx, sess, msgs...); err != nil {
		t.Fatal(err)
	}

	sm, ok := p.(memory.SessionManager)
	if !ok {
		t.Skip("LCM provider does not implement SessionManager")
	}

	history, err := sm.LoadHistory(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 2 {
		t.Errorf("expected at least 2 messages in history, got %d", len(history))
	}
}
