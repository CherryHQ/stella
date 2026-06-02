package lcm_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
)

func newLCMTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	_, err = db.Exec(`INSERT INTO agent (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled)
		VALUES ('test', 'Test Agent', '', '', '', '', '', 'system', '', 1)`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed agent: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO auth_user (id, email) VALUES ('1', 'user-1@test.local')`); err != nil {
		_ = db.Close()
		t.Fatalf("seed user: %v", err)
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
		UserID:  "1",
		Channel: "cli",
	}
}

func TestLCMProvider_RequiresSessionUserAndAgentScope(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	ctx := context.Background()
	base := newLCMTestSession("missing-scope")
	cases := []struct {
		name string
		sess memory.Session
	}{
		{name: "missing user", sess: memory.Session{ID: base.ID, AgentID: base.AgentID, Channel: base.Channel}},
		{name: "missing agent", sess: memory.Session{ID: base.ID, UserID: base.UserID, Channel: base.Channel}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.Bootstrap(ctx, tc.sess); err == nil {
				t.Fatal("Bootstrap succeeded without complete scope")
			}
			if err := p.Append(ctx, tc.sess, ai.UserMessage{Content: "hello"}); err == nil {
				t.Fatal("Append succeeded without complete scope")
			}
			if _, err := p.Assemble(ctx, tc.sess, 1000, 0); err == nil {
				t.Fatal("Assemble succeeded without complete scope")
			}
			if _, err := p.Stats(ctx, tc.sess); err == nil {
				t.Fatal("Stats succeeded without complete scope")
			}

			searcher := p.(memory.Searcher)
			if _, err := searcher.Search(ctx, tc.sess, memory.SearchQuery{Text: "hello"}); err == nil {
				t.Fatal("Search succeeded without complete scope")
			}

			reviewer := p.(memory.Reviewer)
			if _, err := reviewer.BuildReviewContext(ctx, tc.sess, time.Time{}); err == nil {
				t.Fatal("BuildReviewContext succeeded without complete scope")
			}

			explorer := p.(memory.Explorer)
			if _, err := explorer.Describe(ctx, "missing-summary"); err == nil {
				t.Fatal("Describe succeeded without complete scope")
			}
			if _, err := explorer.Expand(ctx, "missing-summary", 1000); err == nil {
				t.Fatal("Expand succeeded without complete scope")
			}

			compactor := p.(memory.Compactor)
			if compactor.NeedsCompaction(ctx, tc.sess, 0) {
				t.Fatal("NeedsCompaction returned true without complete scope")
			}
			if _, err := compactor.Compact(ctx, tc.sess, memory.CompactionIncremental); err == nil {
				t.Fatal("Compact succeeded without complete scope")
			}
		})
	}
}

func TestLCMProvider_DoesNotReuseConversationCacheAcrossScope(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	ctx := context.Background()
	sess := newLCMTestSession("scope-cache")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := p.Append(ctx, sess, ai.UserMessage{Content: "private user one message"}); err != nil {
		t.Fatal(err)
	}

	otherUser := sess
	otherUser.UserID = "2"
	got, err := p.Assemble(ctx, otherUser, 100000, 0)
	if err == nil {
		for _, msg := range got {
			if strings.Contains(fmt.Sprint(msg), "private user one message") {
				t.Fatal("Assemble returned another user's conversation")
			}
		}
		t.Fatal("Assemble unexpectedly succeeded for same session ID under a different user")
	}
}

func TestLCMProvider_CompactSummarizerCanReadDB(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { _ = db.Close() }()

	p, err := lcm.New(db, func(ctx context.Context, _ string) (string, error) {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent`).Scan(&count); err != nil {
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
	if err := db.QueryRowContext(ctx, `SELECT content FROM ctx_summary ORDER BY created_at DESC LIMIT 1`).Scan(&content); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if content != "summary from db-backed summarizer" {
		t.Fatalf("summary content = %q", content)
	}
}

func TestLCMProvider_NeedsCompactionWaitsForCompactableMessages(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { _ = db.Close() }()

	p, err := lcm.New(db, func(context.Context, string) (string, error) {
		return "summary", nil
	}, map[string]any{"fresh_tail": 1})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := newLCMTestSession("compact-needs")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("message %02d with enough content", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	var compactor memory.Compactor = p
	if !compactor.NeedsCompaction(ctx, sess, 0) {
		t.Fatal("expected initial compactable session")
	}
	if _, err := compactor.Compact(ctx, sess, memory.CompactionIncremental); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if compactor.NeedsCompaction(ctx, sess, 0) {
		t.Fatal("expected no compaction until enough new messages accumulate")
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

	sess := newLCMTestSession("history")
	ctx := memory.WithAgentID(memory.WithUserID(context.Background(), sess.UserID), sess.AgentID)

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

func TestLCMProvider_ListInfoForReviewDoesNotRequireUserScope(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { _ = db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := memory.WithAgentID(context.Background(), "test")
	if err := p.SaveInfo(ctx, memory.SessionInfo{
		ID:         "review-chat",
		AgentID:    "test",
		UserID:     "1",
		Channel:    "web",
		Kind:       "chat",
		LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveInfo chat: %v", err)
	}
	if err := p.SaveInfo(ctx, memory.SessionInfo{
		ID:         "review-task",
		AgentID:    "test",
		UserID:     "1",
		Channel:    "task",
		Kind:       "task",
		LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveInfo task: %v", err)
	}

	infos, err := p.ListInfoForReview(context.Background(), memory.ListOptions{AgentID: "test"})
	if err != nil {
		t.Fatalf("ListInfoForReview: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2", len(infos))
	}
}
