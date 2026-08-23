package lcm_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// auth_user.id and ctx_agent_memory.user_id are UUID columns, so test user ids
// must be valid UUIDs. These are the two users seeded by newLCMTestDB.
var (
	testUserID      = uuid.NewString()
	testOtherUserID = uuid.NewString()
)

func newLCMTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.New(t)

	_, err := db.Exec(context.Background(), `INSERT INTO agent (id, name, model, model_strong, model_fast, system_prompt, workspace, scope, creator_id, enabled)
		VALUES ('test', 'Test Agent', '', '', '', '', '', 'system', '', true)`)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err = db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ($1, 'user-1@test.local'), ($2, 'user-2@test.local')`, testUserID, testOtherUserID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return db
}

func newLCMTestProvider(t *testing.T) (memory.Provider, func()) {
	t.Helper()
	db := newLCMTestDB(t)

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		db.Close()
		t.Fatalf("new provider: %v", err)
	}
	return p, func() {
		_ = p.Close()
		db.Close()
	}
}

func sessionIDs(infos []memory.SessionInfo) []string {
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids
}

func newLCMTestSession(suffix string) memory.Session {
	return memory.Session{
		ID:      "test:cli:1:" + suffix,
		AgentID: "test",
		UserID:  testUserID,
		Channel: "cli",
	}
}

func TestSessionActivityWatermarksRoundTrip(t *testing.T) {
	provider, cleanup := newLCMTestProvider(t)
	defer cleanup()
	manager := provider.(memory.SessionManager)
	activity := provider.(memory.SessionActivityStore)
	session := newLCMTestSession("activity")
	ctx := authz.WithAgentID(authz.WithUserID(t.Context(), session.UserID), session.AgentID)
	now := time.Now().UTC()
	if err := manager.SaveInfo(ctx, memory.SessionInfo{
		ID: session.ID, AgentID: session.AgentID, UserID: session.UserID,
		Channel: session.Channel, Kind: "chat", CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	if updated, err := activity.MarkSessionTurnStarted(ctx, session); err != nil || !updated {
		t.Fatalf("MarkSessionTurnStarted: updated=%v err=%v", updated, err)
	}
	started, err := manager.LoadInfo(ctx, session.ID)
	if err != nil {
		t.Fatalf("LoadInfo after start: %v", err)
	}
	if started.LastTurnStartedAt.IsZero() || !started.LastTurnCompletedAt.IsZero() {
		t.Fatalf("activity after start = %+v", started)
	}

	if updated, err := activity.MarkSessionTurnCompleted(ctx, session, memory.SessionTurnSuccess); err != nil || !updated {
		t.Fatalf("MarkSessionTurnCompleted: updated=%v err=%v", updated, err)
	}
	completed, err := manager.LoadInfo(ctx, session.ID)
	if err != nil {
		t.Fatalf("LoadInfo after completion: %v", err)
	}
	if completed.LastTurnCompletedAt.Before(completed.LastTurnStartedAt) {
		t.Fatalf("completion %v precedes start %v", completed.LastTurnCompletedAt, completed.LastTurnStartedAt)
	}
	if completed.LastTurnResult != memory.SessionTurnSuccess {
		t.Fatalf("completion result = %q, want success", completed.LastTurnResult)
	}

	if updated, err := activity.MarkSessionViewed(ctx, session); err != nil || !updated {
		t.Fatalf("MarkSessionViewed: updated=%v err=%v", updated, err)
	}
	viewed, err := manager.LoadInfo(ctx, session.ID)
	if err != nil {
		t.Fatalf("LoadInfo after view: %v", err)
	}
	if viewed.LastViewedAt.Before(viewed.LastTurnCompletedAt) {
		t.Fatalf("view %v precedes completion %v", viewed.LastViewedAt, viewed.LastTurnCompletedAt)
	}
}

func TestTouchKnowledgeUsageDoesNotRecreateMissingRuntimeRow(t *testing.T) {
	db := newLCMTestDB(t)
	defer db.Close()
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	fact, err := memorywrite.CreateFact(ctx, db, sqlc.New(db), memory.FactWrite{
		UserID: testUserID, AgentID: "test", Subject: memory.FactSubjectWorld,
		Content: "Runtime touch must not recreate usage.", Source: memory.SourceReflect,
	})
	if err != nil {
		t.Fatalf("create reflect fact: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM knowledge_usage WHERE fact_id = $1`, fact.ID); err != nil {
		t.Fatalf("delete knowledge usage: %v", err)
	}

	if err := p.TouchKnowledgeUsage(ctx, testUserID, "test", []string{fact.ID}); err != nil {
		t.Fatalf("TouchKnowledgeUsage: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM knowledge_usage WHERE fact_id = $1`, fact.ID).Scan(&count); err != nil {
		t.Fatalf("count knowledge usage: %v", err)
	}
	if count != 0 {
		t.Fatalf("knowledge usage rows = %d, runtime touch must be UPDATE-only", count)
	}
}

func TestLCMProvider_StatsUsesConversationTimeBounds(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	emptySess := newLCMTestSession("stats-empty")
	if err := p.Bootstrap(ctx, emptySess); err != nil {
		t.Fatalf("bootstrap empty: %v", err)
	}
	emptyStats, err := p.Stats(ctx, emptySess)
	if err != nil {
		t.Fatalf("stats empty: %v", err)
	}
	if !emptyStats.OldestAt.IsZero() || !emptyStats.NewestAt.IsZero() {
		t.Fatalf("empty bounds = %v/%v, want zero times", emptyStats.OldestAt, emptyStats.NewestAt)
	}

	sess := newLCMTestSession("stats-bounds")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "first"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "middle"}}},
		ai.UserMessage{Content: "last"},
	); err != nil {
		t.Fatalf("append: %v", err)
	}

	oldest := "2024-01-01 01:02:03"
	newest := "2024-01-03 04:05:06"
	if _, err := db.Exec(ctx, `
		UPDATE ctx_message
		SET created_at = CASE seq
			WHEN 1 THEN $1
			WHEN 2 THEN '2024-01-02 02:03:04'
			WHEN 3 THEN $2
			ELSE created_at
		END
		WHERE conversation_id = (SELECT id FROM ctx_conversation WHERE session_id = $3)
	`, oldest, newest, sess.ID); err != nil {
		t.Fatalf("set message timestamps: %v", err)
	}

	stats, err := p.Stats(ctx, sess)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	wantOldest, _ := time.Parse("2006-01-02 15:04:05", oldest)
	wantNewest, _ := time.Parse("2006-01-02 15:04:05", newest)
	if !stats.OldestAt.Equal(wantOldest) || !stats.NewestAt.Equal(wantNewest) {
		t.Fatalf("bounds = %v/%v, want %v/%v", stats.OldestAt, stats.NewestAt, wantOldest, wantNewest)
	}
}

func TestLCMProvider_RequiresSessionUserAndAgentScope(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	lcmProvider, err := lcm.New(db, func(context.Context, string) (string, error) { return "summary", nil }, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = lcmProvider.Close() }()
	var p memory.Provider = lcmProvider

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
	otherUser.UserID = testOtherUserID
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
	defer func() { db.Close() }()

	p, err := lcm.New(db, func(ctx context.Context, _ string) (string, error) {
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM agent`).Scan(&count); err != nil {
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
	if err := db.QueryRow(ctx, `SELECT content FROM ctx_summary ORDER BY created_at DESC LIMIT 1`).Scan(&content); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if content != "summary from db-backed summarizer" {
		t.Fatalf("summary content = %q", content)
	}
}

func TestLCMProvider_NilSummarizerDisablesCompaction(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := newLCMTestSession("nil-summarizer")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for i := range 16 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("message %02d with enough content", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	var compactor memory.Compactor = p
	if compactor.NeedsCompaction(ctx, sess, 0) {
		t.Fatal("nil summarizer should disable NeedsCompaction")
	}
	result, err := compactor.Compact(ctx, sess, memory.CompactionIncremental)
	if err != nil {
		t.Fatalf("compact with nil summarizer: %v", err)
	}
	if result != nil {
		t.Fatalf("compact with nil summarizer result = %#v, want nil", result)
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_summary`).Scan(&count); err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	if count != 0 {
		t.Fatalf("ctx_summary rows = %d, want 0", count)
	}
}

func TestLCMProvider_NeedsCompactionWaitsForCompactableMessages(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

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

// Append takes the canonical message route in production. Keep this separate
// from toolResultToRows: a helper-only test once let the provider silently drop
// Code Mode's provider-invisible audit metadata.
func TestLCMProvider_AppendCanonicalToolResultPersistsChildAudit(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	ctx := context.Background()
	sess := newLCMTestSession("canonical-child-audit")
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := p.Append(ctx, sess,
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "outer", Name: "code"}}},
		ai.ToolResultMessage{
			ToolCallID: "outer", ToolName: "code", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			ChildToolCalls: []ai.ChildToolCallAudit{{ID: "outer:1", Name: "bash", IsError: true, ErrorKind: ai.ToolErrorKindTool}},
		},
	); err != nil {
		t.Fatal(err)
	}
	got, err := p.Assemble(ctx, sess, 100_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range got {
		result, ok := message.(ai.ToolResultMessage)
		if ok && result.ToolCallID == "outer" {
			if len(result.ChildToolCalls) != 1 || result.ChildToolCalls[0].Name != "bash" || !result.ChildToolCalls[0].IsError {
				t.Fatalf("canonical append child audit = %#v", result.ChildToolCalls)
			}
			return
		}
	}
	t.Fatalf("canonical tool result missing from assembled history: %#v", got)
}

func TestLCMProvider_LoadHistory(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	sess := newLCMTestSession("history")
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)

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

func TestLCMProvider_LoadReviewHistoryPreservesSeqBoundary(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	sess := newLCMTestSession("review-history")
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}

	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "hello"},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "working"},
			ai.ToolCall{ID: "tc1", Name: "shell", Arguments: map[string]any{"cmd": "true"}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	reader, ok := p.(memory.ReviewHistoryReader)
	if !ok {
		t.Fatal("LCM provider does not implement ReviewHistoryReader")
	}
	history, err := reader.LoadReviewHistory(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 logical review messages, got %#v", history)
	}
	if history[0].FirstSeq != 1 || history[0].LastSeq != 1 {
		t.Fatalf("expected user message seq 1..1, got %#v", history[0])
	}
	if history[1].FirstSeq != 2 || history[1].LastSeq != 3 {
		t.Fatalf("expected assistant message seq 2..3, got %#v", history[1])
	}
}

func TestLCMProvider_ListInfoForReviewIncludesLatestSeq(t *testing.T) {
	p, cleanup := newLCMTestProvider(t)
	defer cleanup()

	sess := newLCMTestSession("review-latest-seq")
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), sess.UserID), sess.AgentID)
	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "hello"},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "working"},
			ai.ToolCall{ID: "tc1", Name: "shell", Arguments: map[string]any{"cmd": "true"}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	lister, ok := p.(interface {
		ListInfoForReview(context.Context, memory.ListOptions) ([]memory.SessionInfo, error)
	})
	if !ok {
		t.Fatal("LCM provider does not implement review listing")
	}
	infos, err := lister.ListInfoForReview(context.Background(), memory.ListOptions{AgentID: sess.AgentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == sess.ID {
			if info.LatestSeq != 3 {
				t.Fatalf("LatestSeq = %d, want 3", info.LatestSeq)
			}
			return
		}
	}
	t.Fatalf("session %q not found in review listing", sess.ID)
}

func TestLCMProvider_ListInfoFiltersInSQL(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), testUserID), "test")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixtures := []memory.SessionInfo{
		{ID: "list-1", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(1 * time.Minute)},
		{ID: "list-2", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "task", ProjectID: "p1", LastActive: base.Add(2 * time.Minute)},
		{ID: "list-3", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p2", LastActive: base.Add(3 * time.Minute)},
		{ID: "list-4", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(4 * time.Minute)},
		{ID: "list-5", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(5 * time.Minute), Archived: true},
		{ID: "list-6", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(6 * time.Minute)},
		{ID: "list-7", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", LastActive: base.Add(7 * time.Minute)},
	}
	for _, info := range fixtures {
		if err := p.SaveInfo(ctx, info); err != nil {
			t.Fatalf("SaveInfo %s: %v", info.ID, err)
		}
	}

	infos, err := p.ListInfo(ctx, memory.ListOptions{Kind: "chat", ProjectID: "p1", Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListInfo filtered: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"list-4", "list-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered IDs = %v, want %v", got, want)
	}

	infos, err = p.ListInfo(ctx, memory.ListOptions{Kind: "chat", ProjectID: "p1", IncludeArchived: true, Limit: 3})
	if err != nil {
		t.Fatalf("ListInfo include archived: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"list-6", "list-5", "list-4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("include archived IDs = %v, want %v", got, want)
	}

	infos, err = p.ListInfo(ctx, memory.ListOptions{Kind: "chat", ProjectIDIsNull: true})
	if err != nil {
		t.Fatalf("ListInfo project null: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"list-7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project null IDs = %v, want %v", got, want)
	}
}

// TestLCMProvider_ListInfoFiltersByChannel covers the durable channel binding:
// chat-channel sessions are found by their channel rather than by a key-derived
// id, and a row written before the binding existed adopts one on first save
// without ever overwriting a channel that is already set.
func TestLCMProvider_ListInfoFiltersByChannel(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), testUserID), "test")
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	fixtures := []memory.SessionInfo{
		{ID: "chan-1", AgentID: "test", UserID: testUserID, Channel: "tg:private:1", Kind: "chat", LastActive: base.Add(1 * time.Minute)},
		{ID: "chan-2", AgentID: "test", UserID: testUserID, Channel: "tg:private:2", Kind: "chat", LastActive: base.Add(2 * time.Minute)},
		{ID: "chan-3", AgentID: "test", UserID: testUserID, Channel: "tg:private:1", Kind: "chat", LastActive: base.Add(3 * time.Minute)},
		{ID: "chan-legacy", AgentID: "test", UserID: testUserID, Kind: "chat", LastActive: base.Add(4 * time.Minute)},
	}
	for _, info := range fixtures {
		if err := p.SaveInfo(ctx, info); err != nil {
			t.Fatalf("SaveInfo %s: %v", info.ID, err)
		}
	}

	infos, err := p.ListInfo(ctx, memory.ListOptions{Kind: "chat", Channel: "tg:private:1"})
	if err != nil {
		t.Fatalf("ListInfo by channel: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"chan-3", "chan-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel-filtered IDs = %v, want %v", got, want)
	}

	// A blank stored channel adopts the supplied one exactly once.
	legacy := fixtures[3]
	legacy.Channel = "tg:private:9"
	legacy.LastActive = base.Add(5 * time.Minute)
	if err := p.SaveInfo(ctx, legacy); err != nil {
		t.Fatalf("SaveInfo adopting a channel: %v", err)
	}
	infos, err = p.ListInfo(ctx, memory.ListOptions{Kind: "chat", Channel: "tg:private:9"})
	if err != nil {
		t.Fatalf("ListInfo after adoption: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"chan-legacy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("adopted IDs = %v, want %v", got, want)
	}

	// A set channel is never overwritten: the binding a session was resolved by
	// must not move under a later save.
	legacy.Channel = "tg:private:other"
	if err := p.SaveInfo(ctx, legacy); err != nil {
		t.Fatalf("SaveInfo rebinding attempt: %v", err)
	}
	infos, err = p.ListInfo(ctx, memory.ListOptions{Kind: "chat", Channel: "tg:private:9"})
	if err != nil {
		t.Fatalf("ListInfo after rebinding attempt: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"chan-legacy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel changed under an existing binding: %v, want %v", got, want)
	}
}

func TestLCMProvider_SaveInfoSingleUpdateSemantics(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), testUserID), "test")
	initial := memory.SessionInfo{ID: "save-info", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", Title: "Original", LastActive: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := p.SaveInfo(ctx, initial); err != nil {
		t.Fatalf("SaveInfo initial: %v", err)
	}
	if err := p.SaveInfo(ctx, memory.SessionInfo{ID: "save-info", AgentID: "test", UserID: testUserID, Title: "Renamed", Kind: "task", ProjectID: "p2", Archived: true}); err != nil {
		t.Fatalf("SaveInfo metadata: %v", err)
	}
	info, err := p.LoadInfo(ctx, "save-info")
	if err != nil {
		t.Fatalf("LoadInfo metadata: %v", err)
	}
	if info.Title != "Renamed" || info.Archived || info.Kind != "task" || info.ProjectID != "p2" {
		t.Fatalf("metadata update changed lifecycle or missed fields: %+v", info)
	}

	// Capture a stale active snapshot, archive through the dedicated transition,
	// then replay it. The guarded metadata UPDATE must match zero rows and leave
	// the archived lifecycle state untouched.
	stale := info
	applied, err := p.ArchiveInfo(ctx, info)
	if err != nil || !applied {
		t.Fatalf("ArchiveInfo: applied=%v err=%v", applied, err)
	}
	stale.Title = "Stale rename"
	if err := p.SaveInfo(ctx, stale); !errors.Is(err, memory.ErrInactiveSession) {
		t.Fatalf("SaveInfo stale snapshot = %v, want ErrInactiveSession", err)
	}
	info, err = p.LoadInfo(ctx, "save-info")
	if err != nil {
		t.Fatalf("LoadInfo archived: %v", err)
	}
	if !info.Archived || info.Title != "Renamed" {
		t.Fatalf("stale metadata save resurrected or changed archived row: %+v", info)
	}
	applied, err = p.ArchiveInfo(ctx, info)
	if err != nil || applied {
		t.Fatalf("second ArchiveInfo: applied=%v err=%v, want no-op", applied, err)
	}
}

// TestLCMProvider_SaveInfoCannotResurrectConcurrentArchive forces SaveInfo to
// read an active snapshot while an uncommitted archive owns the row lock. After
// the archive commits, PostgreSQL rechecks SaveInfo's archived=false predicate;
// the stale metadata write must affect zero rows instead of reviving the row.
func TestLCMProvider_SaveInfoCannotResurrectConcurrentArchive(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), testUserID), "test")
	stale := memory.SessionInfo{ID: "save-info-race", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", Title: "Before archive"}
	if err := p.SaveInfo(ctx, stale); err != nil {
		t.Fatalf("SaveInfo initial: %v", err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin archive: %v", err)
	}
	archiveOpen := true
	defer func() {
		if archiveOpen {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := tx.Exec(ctx, `UPDATE ctx_conversation SET archived = true WHERE session_id = $1 AND user_id = $2 AND agent_id = $3`, stale.ID, stale.UserID, stale.AgentID); err != nil {
		t.Fatalf("stage archive: %v", err)
	}

	stale.Title = "Stale rename"
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- p.SaveInfo(ctx, stale)
	}()

	// Observe the metadata UPDATE waiting on the archive transaction before
	// committing it; this pins the intended interleaving instead of relying on a
	// scheduler sleep.
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err := db.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%UPDATE ctx_conversation%'
		)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked metadata save: %v", err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		_ = tx.Rollback(ctx)
		archiveOpen = false
		<-saveDone
		t.Fatal("SaveInfo never reached the archive row lock")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit archive: %v", err)
	}
	archiveOpen = false
	if err := <-saveDone; !errors.Is(err, memory.ErrInactiveSession) {
		t.Fatalf("concurrent SaveInfo = %v, want ErrInactiveSession", err)
	}

	info, err := p.LoadInfo(ctx, stale.ID)
	if err != nil {
		t.Fatalf("LoadInfo archived: %v", err)
	}
	if !info.Archived || info.Title != "Before archive" {
		t.Fatalf("concurrent metadata save resurrected or changed archived row: %+v", info)
	}
}

func TestLCMProvider_ListInfoForReviewFiltersInSQL(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixtures := []memory.SessionInfo{
		{ID: "review-1", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(1 * time.Minute)},
		{ID: "review-2", AgentID: "test", UserID: testOtherUserID, Channel: "web", Kind: "task", ProjectID: "p1", LastActive: base.Add(2 * time.Minute)},
		{ID: "review-3", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p2", LastActive: base.Add(3 * time.Minute)},
		{ID: "review-4", AgentID: "test", UserID: testOtherUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(4 * time.Minute)},
		{ID: "review-5", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(5 * time.Minute), Archived: true},
		{ID: "review-6", AgentID: "test", UserID: testOtherUserID, Channel: "web", Kind: "chat", ProjectID: "p1", LastActive: base.Add(6 * time.Minute)},
		{ID: "review-7", AgentID: "test", UserID: testUserID, Channel: "web", Kind: "chat", LastActive: base.Add(7 * time.Minute)},
	}
	for _, info := range fixtures {
		if err := p.SaveInfo(context.Background(), info); err != nil {
			t.Fatalf("SaveInfo %s: %v", info.ID, err)
		}
	}

	infos, err := p.ListInfoForReview(context.Background(), memory.ListOptions{AgentID: "test", Kind: "chat", ProjectID: "p1", Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListInfoForReview filtered: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"review-4", "review-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review filtered IDs = %v, want %v", got, want)
	}

	infos, err = p.ListInfoForReview(context.Background(), memory.ListOptions{AgentID: "test", Kind: "chat", ProjectIDIsNull: true})
	if err != nil {
		t.Fatalf("ListInfoForReview project null: %v", err)
	}
	if got, want := sessionIDs(infos), []string{"review-7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review project null IDs = %v, want %v", got, want)
	}
}

func TestLCMProvider_ListInfoForReviewDoesNotRequireUserScope(t *testing.T) {
	db := newLCMTestDB(t)
	defer func() { db.Close() }()

	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := authz.WithAgentID(context.Background(), "test")
	if err := p.SaveInfo(ctx, memory.SessionInfo{
		ID:         "review-chat",
		AgentID:    "test",
		UserID:     testUserID,
		Channel:    "web",
		Kind:       "chat",
		LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveInfo chat: %v", err)
	}
	if err := p.SaveInfo(ctx, memory.SessionInfo{
		ID:         "review-task",
		AgentID:    "test",
		UserID:     testUserID,
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
