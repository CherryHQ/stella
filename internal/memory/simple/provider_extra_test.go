package simple_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/simple"
	"github.com/CherryHQ/stella/pkg/ai"
)

func newTestDB(t *testing.T) (*simple.Provider, func()) {
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
		t.Fatalf("seed agent: %v", err)
	}

	p := simple.New(db)
	return p, func() {
		_ = p.Close()
		_ = db.Close()
	}
}

func newTestSession() memory.Session {
	return memory.Session{
		ID:      "test:cli:1:main-extra",
		AgentID: "test",
		UserID:  "1",
		Channel: "cli",
	}
}

func TestSimpleProviderRequiresSessionScope(t *testing.T) {
	p, cleanup := newTestDB(t)
	defer cleanup()

	sess := memory.Session{ID: "test:cli:missing-scope", UserID: "1", Channel: "cli"}
	if err := p.Bootstrap(context.Background(), sess); err == nil {
		t.Fatal("expected missing agent context error")
	}
}

func TestSimpleProviderUsesContextSessionScope(t *testing.T) {
	p, cleanup := newTestDB(t)
	defer cleanup()

	ctx := memory.WithAgentID(memory.WithUserID(context.Background(), "1"), "test")
	sess := memory.Session{ID: "test:cli:context-scope", Channel: "cli"}
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	info, err := p.LoadInfo(ctx, sess.ID)
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if info.UserID != "1" || info.AgentID != "test" {
		t.Fatalf("scope = user %q agent %q, want user 1 agent test", info.UserID, info.AgentID)
	}
}

func TestSimpleProvider_AppendAssistantMessage(t *testing.T) {
	p, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sess := newTestSession()

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	msgs := []ai.Message{
		ai.UserMessage{Content: "hello", Timestamp: time.Now()},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "world"}},
			StopReason: ai.StopReasonStop,
			Timestamp:  time.Now(),
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

func TestSimpleProvider_AppendToolResultMessage(t *testing.T) {
	p, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sess := newTestSession()
	sess.ID = "test:cli:1:main-toolresult"

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	msgs := []ai.Message{
		ai.UserMessage{Content: "hello", Timestamp: time.Now()},
		ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "tc1", Name: "bash", Arguments: map[string]any{"command": "ls"}},
			},
			StopReason: ai.StopReasonToolUse,
			Timestamp:  time.Now(),
		},
		ai.ToolResultMessage{
			ToolCallID: "tc1",
			Content:    []ai.ContentBlock{ai.TextContent{Text: "file.txt"}},
			Timestamp:  time.Now(),
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

func TestSimpleProvider_LoadHistory_WithMixedMessages(t *testing.T) {
	p, cleanup := newTestDB(t)
	defer cleanup()

	sess := newTestSession()
	ctx := memory.WithAgentID(memory.WithUserID(context.Background(), sess.UserID), sess.AgentID)
	sess.ID = "test:cli:1:main-history"

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	msgs := []ai.Message{
		ai.UserMessage{Content: "hello", Timestamp: now},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "hi there"}},
			StopReason: ai.StopReasonStop,
			Timestamp:  now.Add(time.Second),
		},
	}

	if err := p.Append(ctx, sess, msgs...); err != nil {
		t.Fatal(err)
	}

	var sm memory.SessionManager
	if s, ok := memory.Provider(p).(memory.SessionManager); ok {
		sm = s
	} else {
		t.Skip("provider does not implement SessionManager")
	}

	history, err := sm.LoadHistory(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 2 {
		t.Errorf("expected at least 2 messages in history, got %d", len(history))
	}
}
