package lcm

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.New(t)
}

func groupSess(agentID, groupID string) memory.Session {
	return memory.Session{
		ID:      agentID + ":group:" + groupID,
		AgentID: agentID,
		UserID:  groupID,
		GroupID: groupID,
	}
}

func groupCtx(triggerSeq int64) context.Context {
	ctx := context.Background()
	ctx = authz.WithAgentID(ctx, "agent-a")
	if triggerSeq > 0 {
		ctx = memory.WithGroupSeq(ctx, triggerSeq)
	}
	return ctx
}

func assembleGroupForTest(
	t *testing.T,
	p *Provider,
	ctx context.Context,
	session memory.Session,
	budget int,
	freshTail int,
) []ai.Message {
	t.Helper()
	if err := p.SyncGroupEventsBefore(ctx, session, memory.GroupSeqFromContext(ctx)); err != nil {
		t.Fatalf("sync group events: %v", err)
	}
	msgs, err := p.Assemble(ctx, session, budget, freshTail)
	if err != nil {
		t.Fatalf("assemble group: %v", err)
	}
	return msgs
}

func mustGroupCursor(t *testing.T, p *Provider, groupID, pipeline string) int64 {
	t.Helper()
	cursor, err := p.getGroupCursor(context.Background(), groupID, pipeline)
	if err != nil {
		t.Fatalf("get group cursor: %v", err)
	}
	return cursor
}

func TestGroupAssemble_HybridFlow(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	// Seed: user1 says "hello" (seq=1), user2 says "hey" (seq=2).
	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g1", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "hello", PlatformMessageID: "m1",
	})
	if err != nil {
		t.Fatalf("seed m1: %v", err)
	}
	gid := res1.GroupID

	res2, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g1", ActorType: eventlog.ActorHuman,
		ActorID: "user2", Content: "hey", PlatformMessageID: "m2",
	})
	if err != nil {
		t.Fatalf("seed m2: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sess := groupSess("agent-a", gid)

	// Turn 1: triggered by user2's message (seq=2).
	// Between-turn injection: seq > 0 (watermark) AND seq < 2 → seq=1 (user1's "hello").
	assembleCtx := groupCtx(res2.Seq)
	msgs := assembleGroupForTest(t, p, assembleCtx, sess, 100_000, 20)

	// Should inject user1's "hello" (seq=1). user2's "hey" (seq=2=triggerSeq) is excluded.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 injected message, got %d", len(msgs))
	}
	assertRole(t, msgs[0], "user")
	text := flattenUserMessage(msgs[0].(ai.UserMessage))
	if text != "[seq:1 user1]: hello" {
		t.Fatalf("injected text = %q, want [seq:1 user1]: hello", text)
	}

	// Simulate agent processing: append user msg + assistant response to ctx_message.
	if err := p.Append(assembleCtx, sess,
		ai.UserMessage{Content: "hey"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "Hi there!"}}},
	); err != nil {
		t.Fatalf("append: %v", err)
	}

	if err := p.CommitGroupCursor(context.Background(), sess, res2.Seq); err != nil {
		t.Fatalf("commit cursor turn 1: %v", err)
	}

	// Agent response written back to event log (seq=3).
	_, err = el.AppendToGroup(ctx, gid, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "agent-a", Content: "Hi there!",
	})
	if err != nil {
		t.Fatalf("writeback: %v", err)
	}

	// Between turns: user1 sends "what's up?" (seq=4).
	res4, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g1", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "what's up?", PlatformMessageID: "m4",
	})
	if err != nil {
		t.Fatalf("seed m4: %v", err)
	}

	// Turn 2: triggered by seq=4.
	// Between-turn injection: seq > 2 (watermark) AND seq < 4 → seq=3 (agent-a, skipped as self).
	assembleCtx2 := groupCtx(res4.Seq)
	msgs2 := assembleGroupForTest(t, p, assembleCtx2, sess, 100_000, 20)

	// Should have: persisted injected [user1]: hello from turn 1, plus ctx_message [user: "hey", assistant: "Hi there!"].
	// seq=3 (agent-a self) is skipped in between-turn.
	if len(msgs2) != 3 {
		t.Fatalf("expected 3 messages (1 persisted injected + 2 agent history), got %d", len(msgs2))
	}
	assertRole(t, msgs2[0], "user")
	assertRole(t, msgs2[1], "user")
	assertRole(t, msgs2[2], "assistant")
}

func TestGroupAssemble_OtherAgentInjected(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	// user1 says hello (seq=1).
	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g2", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "hello", PlatformMessageID: "m1",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	gid := res1.GroupID

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sess := groupSess("agent-a", gid)

	// Turn 1: trigger = seq=1. No between-turn messages.
	assembleCtx := groupCtx(res1.Seq)
	if err := p.Bootstrap(assembleCtx, sess); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	msgs := assembleGroupForTest(t, p, assembleCtx, sess, 100_000, 20)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages on first trigger, got %d", len(msgs))
	}

	// Simulate turn 1 processing.
	if err := p.Append(assembleCtx, sess,
		ai.UserMessage{Content: "hello"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "hi"}}},
	); err != nil {
		t.Fatalf("append: %v", err)
	}

	if err := p.CommitGroupCursor(context.Background(), sess, res1.Seq); err != nil {
		t.Fatalf("commit cursor turn 1: %v", err)
	}

	// Agent-a response → event log seq=2.
	_, err = el.AppendToGroup(ctx, gid, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "agent-a", Content: "hi",
	})
	if err != nil {
		t.Fatalf("writeback a: %v", err)
	}

	// Agent-b responds → event log seq=3.
	_, err = el.AppendToGroup(ctx, gid, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "agent-b", Content: "I'm agent B",
	})
	if err != nil {
		t.Fatalf("writeback b: %v", err)
	}

	// User sends again → event log seq=4.
	res4, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g2", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "thanks", PlatformMessageID: "m4",
	})
	if err != nil {
		t.Fatalf("seed m4: %v", err)
	}

	// Turn 2: trigger = seq=4.
	// Between-turn: seq 2 (agent-a, skip), seq 3 (agent-b, inject).
	assembleCtx2 := groupCtx(res4.Seq)
	msgs2 := assembleGroupForTest(t, p, assembleCtx2, sess, 100_000, 20)

	// Should have: agent history [user:"hello", assistant:"hi"] + injected [agent-b].
	if len(msgs2) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs2))
	}
	assertRole(t, msgs2[0], "user")      // agent history: user "hello"
	assertRole(t, msgs2[1], "assistant") // agent history: assistant "hi"
	assertRole(t, msgs2[2], "user")      // injected: agent-b

	text := flattenUserMessage(msgs2[2].(ai.UserMessage))
	if text != "[seq:3 agent:agent-b]: I'm agent B" {
		t.Fatalf("injected text = %q", text)
	}
}

func TestGroupAssemble_DedupsPersistedInjectedOutsideBudget(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()
	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{Platform: "test", PlatformGroupID: "g-dedup", ActorType: eventlog.ActorHuman, ActorID: "user1", Content: "already persisted", PlatformMessageID: "d1"})
	if err != nil {
		t.Fatalf("seed seq1: %v", err)
	}
	gid := res1.GroupID
	res2, err := el.AppendGroupMessage(ctx, eventlog.Message{Platform: "test", PlatformGroupID: "g-dedup", ActorType: eventlog.ActorHuman, ActorID: "user2", Content: "trigger", PlatformMessageID: "d2"})
	if err != nil {
		t.Fatalf("seed trigger: %v", err)
	}
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sess := groupSess("agent-a", gid)
	assembleCtx := groupCtx(res2.Seq)
	_ = assembleGroupForTest(t, p, assembleCtx, sess, 100_000, 20)
	for i := range 20 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("large retry filler %02d %s", i, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")}); err != nil {
			t.Fatalf("append filler %d: %v", i, err)
		}
	}
	_ = assembleGroupForTest(t, p, assembleCtx, sess, 20, 20)
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_message WHERE role = 'user' AND content = $1`, "[seq:1 user1]: already persisted").Scan(&count); err != nil {
		t.Fatalf("count persisted injected: %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted injected count = %d, want 1", count)
	}
}

func TestGroupAssemble_TokenBudget(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	// Seed several messages.
	res, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g3", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "a]a long message that consumes tokens", PlatformMessageID: "m1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	gid := res.GroupID
	for i := 2; i <= 5; i++ {
		if _, err := el.AppendGroupMessage(ctx, eventlog.Message{
			Platform: "test", PlatformGroupID: "g3", ActorType: eventlog.ActorHuman,
			ActorID: "user1", Content: "another message with some content",
			PlatformMessageID: fmt.Sprintf("m%d", i),
		}); err != nil {
			t.Fatalf("seed m%d: %v", i, err)
		}
	}

	// Trigger is seq=6 (a new message after the 5 seeded).
	res6, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g3", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "trigger", PlatformMessageID: "m6",
	})
	if err != nil {
		t.Fatalf("seed trigger: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sess := groupSess("agent-a", gid)

	// Very small budget.
	assembleCtx := groupCtx(res6.Seq)
	msgs := assembleGroupForTest(t, p, assembleCtx, sess, 20, 20)
	if len(msgs) >= 5 {
		t.Fatalf("expected fewer than 5 messages with tight budget, got %d", len(msgs))
	}
}

func TestGroupAssemble_EmptyGroup(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// A group session's conversation now carries an FK-backed group_id, so the
	// group must exist even when it has no messages. Create an empty group state.
	gid := uuid.Must(uuid.NewV7()).String()
	if _, err := p.q.CreateGroupState(context.Background(), sqlc.CreateGroupStateParams{
		ID: gid, Platform: "test", PlatformGroupID: "empty-" + gid,
	}); err != nil {
		t.Fatalf("create group state: %v", err)
	}

	sess := groupSess("agent-a", gid)
	assembleCtx := groupCtx(1)
	msgs := assembleGroupForTest(t, p, assembleCtx, sess, 100_000, 20)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for empty group, got %d", len(msgs))
	}
}

func TestGroupAppend_StoresMessages(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	el := eventlog.NewStore(db)
	gid, err := el.ResolveGroupID(context.Background(), "test", "g1", "")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}

	sess := groupSess("agent-a", gid)
	ctx := groupCtx(0)

	if err := p.Bootstrap(ctx, sess); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	err = p.Append(ctx, sess,
		ai.UserMessage{Content: "test user message"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "test reply"}}},
	)
	if err != nil {
		t.Fatalf("group append should store messages, got: %v", err)
	}

	// Verify messages are in ctx_message by assembling (standard path).
	msgs := assembleGroupForTest(t, p, ctx, sess, 100_000, 20)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages from ctx_message, got %d", len(msgs))
	}
	assertRole(t, msgs[0], "user")
	assertRole(t, msgs[1], "assistant")
}

func TestGroupBootstrap_CreatesConversation(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	el := eventlog.NewStore(db)
	gid, err := el.ResolveGroupID(context.Background(), "test", "g1", "")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}

	sess := groupSess("agent-a", gid)
	ctx := groupCtx(0)

	err = p.Bootstrap(ctx, sess)
	if err != nil {
		t.Fatalf("group bootstrap should create conversation, got: %v", err)
	}

	// Bootstrap again should be idempotent.
	err = p.Bootstrap(ctx, sess)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
}

func TestGroupNeedsCompactionDisabledWithoutSummarizer(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	el := eventlog.NewStore(db)
	gid, err := el.ResolveGroupID(context.Background(), "test", "g1", "")
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}

	sess := groupSess("agent-a", gid)
	if p.NeedsCompaction(context.Background(), sess, 1.0) {
		t.Fatal("compaction should stay disabled without a summarizer")
	}
}

func TestGroupAssemble_WatermarkAdvances(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	// user1 says "a" (seq=1), user2 says "b" (seq=2), user1 says "c" (seq=3).
	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "gw", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "a", PlatformMessageID: "w1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	gid := res1.GroupID
	if _, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "gw", ActorType: eventlog.ActorHuman,
		ActorID: "user2", Content: "b", PlatformMessageID: "w2",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res3, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "gw", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "c", PlatformMessageID: "w3",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sess := groupSess("agent-a", gid)

	// Turn 1: trigger=seq3. Between-turn: seq1, seq2.
	msgs := assembleGroupForTest(t, p, groupCtx(res3.Seq), sess, 100_000, 20)
	if len(msgs) != 2 {
		t.Fatalf("turn 1: expected 2 injected, got %d", len(msgs))
	}

	if got := mustGroupCursor(t, p, gid, groupCursorPipeline("agent-a")); got != 0 {
		t.Fatalf("assemble advanced cursor before commit: got %d", got)
	}

	// Simulate turn processing.
	if err := p.Append(groupCtx(res3.Seq), sess,
		ai.UserMessage{Content: "c"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "reply"}}},
	); err != nil {
		t.Fatalf("append: %v", err)
	}

	if err := p.CommitGroupCursor(context.Background(), sess, res3.Seq); err != nil {
		t.Fatalf("commit cursor turn 1: %v", err)
	}
	if got := mustGroupCursor(t, p, gid, groupCursorPipeline("agent-a")); got != res3.Seq {
		t.Fatalf("cursor after commit = %d, want %d", got, res3.Seq)
	}

	// New message: user2 says "d" (seq=4).
	res4, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "gw", ActorType: eventlog.ActorHuman,
		ActorID: "user2", Content: "d", PlatformMessageID: "w4",
	})
	if err != nil {
		t.Fatalf("seed d: %v", err)
	}

	// Turn 2: trigger=seq4. Between-turn: seq > 3 AND seq < 4 → nothing.
	msgs2 := assembleGroupForTest(t, p, groupCtx(res4.Seq), sess, 100_000, 20)
	// Should have persisted injected [user1]:a + [user2]:b from turn 1, plus agent history user "c" + assistant "reply".
	if len(msgs2) != 4 {
		t.Fatalf("turn 2: expected 4 (2 persisted injected + 2 agent history), got %d", len(msgs2))
	}
	assertRole(t, msgs2[0], "user")
	assertRole(t, msgs2[1], "user")
	assertRole(t, msgs2[2], "user")
	assertRole(t, msgs2[3], "assistant")
}

func TestGroupAssemblePersistsInjectedButCommitAdvancesCursor(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()
	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "gc", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "before", PlatformMessageID: "c1",
	})
	if err != nil {
		t.Fatalf("seed first: %v", err)
	}
	res2, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "gc", ActorType: eventlog.ActorHuman,
		ActorID: "user2", Content: "trigger", PlatformMessageID: "c2",
	})
	if err != nil {
		t.Fatalf("seed trigger: %v", err)
	}
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sess := groupSess("agent-a", res1.GroupID)
	msgs := assembleGroupForTest(t, p, groupCtx(res2.Seq), sess, 100_000, 20)
	if len(msgs) != 1 {
		t.Fatalf("assemble messages = %d, want 1", len(msgs))
	}
	if got := mustGroupCursor(t, p, res1.GroupID, groupCursorPipeline("agent-a")); got != 0 {
		t.Fatalf("cursor before commit = %d, want 0", got)
	}
	historyBeforeCommit, err := p.assembler.assemble(context.Background(), mustConversationID(t, p, sess), 100_000, 20)
	if err != nil {
		t.Fatalf("assemble raw history before commit: %v", err)
	}
	if len(historyBeforeCommit) != 1 {
		t.Fatalf("persisted messages before commit = %d, want 1", len(historyBeforeCommit))
	}
	if err := p.Append(groupCtx(res2.Seq), sess, ai.UserMessage{Content: "trigger"}); err != nil {
		t.Fatalf("append failed trigger: %v", err)
	}
	retryBeforeCommit := assembleGroupForTest(t, p, groupCtx(res2.Seq), sess, 100_000, 20)
	if len(retryBeforeCommit) != 2 {
		t.Fatalf("retry before commit messages = %d, want persisted injected + trigger without duplicate injected", len(retryBeforeCommit))
	}
	if got := flattenUserMessage(retryBeforeCommit[0].(ai.UserMessage)); got != "[seq:1 user1]: before" {
		t.Fatalf("retry injected text = %q", got)
	}
	if got := flattenUserMessage(retryBeforeCommit[1].(ai.UserMessage)); got != "trigger" {
		t.Fatalf("retry trigger text = %q", got)
	}
	if err := p.CommitGroupCursor(context.Background(), sess, res2.Seq); err != nil {
		t.Fatalf("commit cursor: %v", err)
	}
	if got := mustGroupCursor(t, p, res1.GroupID, groupCursorPipeline("agent-a")); got != res2.Seq {
		t.Fatalf("cursor after commit = %d, want %d", got, res2.Seq)
	}
	historyAfterCommit, err := p.assembler.assemble(context.Background(), mustConversationID(t, p, sess), 100_000, 20)
	if err != nil {
		t.Fatalf("assemble raw history after commit: %v", err)
	}
	if len(historyAfterCommit) != 2 {
		t.Fatalf("persisted messages after commit = %d, want 2", len(historyAfterCommit))
	}
	retryMsgs := assembleGroupForTest(t, p, groupCtx(res2.Seq), sess, 100_000, 20)
	if len(retryMsgs) != 2 {
		t.Fatalf("retry assemble messages = %d, want 2 without duplicate injected", len(retryMsgs))
	}
}

func TestGroupAssemble_TriggerSeqZero(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g0", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "hello", PlatformMessageID: "z1",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	gid := res1.GroupID

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sess := groupSess("agent-a", gid)

	// triggerSeq=0: no GroupSeq on context. Should still assemble without error,
	// just skip event log injection entirely.
	assembleCtx := groupCtx(0)
	msgs := assembleGroupForTest(t, p, assembleCtx, sess, 100_000, 20)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages with triggerSeq=0, got %d", len(msgs))
	}
}

func TestGroupEventSyncPersistsStableOriginIdempotently(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	first, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform:          "test",
		PlatformGroupID:   "origin-sync",
		ActorType:         eventlog.ActorHuman,
		ActorID:           "user-1",
		ActorDisplayName:  "Alice",
		Content:           "public context",
		PlatformMessageID: "origin-1",
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform:          "test",
		PlatformGroupID:   "origin-sync",
		ActorType:         eventlog.ActorHuman,
		ActorID:           "user-2",
		Content:           "trigger",
		PlatformMessageID: "origin-2",
	})
	if err != nil {
		t.Fatalf("append trigger event: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	session := groupSess("agent-a", first.GroupID)
	for range 2 {
		if err := p.SyncGroupEventsBefore(ctx, session, trigger.Seq); err != nil {
			t.Fatalf("sync group events: %v", err)
		}
	}

	convID := mustConversationID(t, p, session)
	rows, err := p.q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		t.Fatalf("list conversation messages: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(rows))
	}
	if got := rows[0].OriginGroupMessageID; !got.Valid || got.String != first.Message.ID {
		t.Fatalf("origin = %#v, want %s", got, first.Message.ID)
	}
	if rows[0].Content != "[seq:1 Alice]: public context" {
		t.Fatalf("content = %q", rows[0].Content)
	}
	if got := mustGroupCursor(t, p, first.GroupID, groupCursorPipeline("agent-a")); got != 0 {
		t.Fatalf("sync advanced cursor to %d before successful turn", got)
	}
}

func TestAppendGroupTurnIsAtomicAndIdempotent(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform:          "test",
		PlatformGroupID:   "atomic-turn",
		ActorType:         eventlog.ActorHuman,
		ActorID:           "user-1",
		ActorDisplayName:  "Alice",
		Content:           "please respond",
		PlatformMessageID: "atomic-1",
	})
	if err != nil {
		t.Fatalf("append trigger event: %v", err)
	}
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	session := groupSess("agent-a", trigger.GroupID)
	reply := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}}
	if err := p.AppendGroupTurn(ctx, session, trigger.Message.ID, ai.UserMessage{Content: "fallback"}, reply); err != nil {
		t.Fatalf("append group turn: %v", err)
	}
	if err := p.AppendGroupTurn(
		ctx,
		session,
		trigger.Message.ID,
		ai.UserMessage{Content: "fallback"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "duplicate"}}},
	); err != nil {
		t.Fatalf("retry group turn: %v", err)
	}

	rows, err := p.q.GetMessagesByConversation(ctx, mustConversationID(t, p, session))
	if err != nil {
		t.Fatalf("list conversation messages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("persisted messages = %d, want trigger + one reply", len(rows))
	}
	if !rows[0].OriginGroupMessageID.Valid || rows[0].OriginGroupMessageID.String != trigger.Message.ID {
		t.Fatalf("trigger origin = %#v", rows[0].OriginGroupMessageID)
	}
	if rows[0].Content != "[seq:1 Alice]: please respond" {
		t.Fatalf("trigger content = %q", rows[0].Content)
	}
	if rows[1].Content != "done" {
		t.Fatalf("reply content = %q, want done", rows[1].Content)
	}
}

func TestAppendGroupTurnPreservesCurrentTriggerImageBlocks(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()
	blocks := []ai.ContentBlock{
		ai.TextContent{Text: "please inspect"},
		ai.ImageContent{Data: "aGk=", MimeType: "image/png"},
	}
	contentBlocks, err := ai.MarshalContentBlocks(blocks)
	if err != nil {
		t.Fatalf("marshal content blocks: %v", err)
	}
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform:          "test",
		PlatformGroupID:   "image-turn",
		ActorType:         eventlog.ActorHuman,
		ActorID:           "user-1",
		ActorDisplayName:  "Alice",
		Content:           "please inspect",
		ContentBlocks:     contentBlocks,
		PlatformMessageID: "image-1",
	})
	if err != nil {
		t.Fatalf("append trigger event: %v", err)
	}
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	session := groupSess("agent-a", trigger.GroupID)
	if err := p.AppendGroupTurn(ctx, session, trigger.Message.ID, ai.UserMessage{}, ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
	}); err != nil {
		t.Fatalf("append group turn: %v", err)
	}

	messages, err := p.Assemble(ctx, session, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble group turn: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("assembled messages = %d, want trigger + reply", len(messages))
	}
	user, ok := messages[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("first message = %T, want ai.UserMessage", messages[0])
	}
	userBlocks, ok := user.Content.([]ai.ContentBlock)
	if !ok || len(userBlocks) != 3 {
		t.Fatalf("trigger content = %#v, want attribution + text + image", user.Content)
	}
	if prefix, ok := userBlocks[0].(ai.TextContent); !ok || prefix.Text != "[seq:1 Alice]:" {
		t.Fatalf("attribution block = %#v", userBlocks[0])
	}
	if !ai.HasImage(userBlocks) {
		t.Fatal("current trigger image was not preserved in per-Agent LCM")
	}
}

func TestGroupCompactionTailProtectsSixNewestPublicInputs(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	var session memory.Session
	for i := 1; i <= 8; i++ {
		result, appendErr := el.AppendGroupMessage(ctx, eventlog.Message{
			Platform:          "test",
			PlatformGroupID:   "compaction-tail",
			ActorType:         eventlog.ActorHuman,
			ActorID:           "user-1",
			Content:           fmt.Sprintf("public-%d", i),
			PlatformMessageID: fmt.Sprintf("tail-%d", i),
		})
		if appendErr != nil {
			t.Fatalf("append event %d: %v", i, appendErr)
		}
		session = groupSess("agent-a", result.GroupID)
		if appendErr := p.AppendGroupTurn(
			ctx,
			session,
			result.Message.ID,
			ai.UserMessage{Content: "fallback"},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("reply-%d", i)}}},
		); appendErr != nil {
			t.Fatalf("append turn %d: %v", i, appendErr)
		}
	}

	convID := mustConversationID(t, p, session)
	items, err := p.q.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("get context items: %v", err)
	}
	tail, older, err := splitCompactionTail(ctx, p.q, convID, items, true, 99)
	if err != nil {
		t.Fatalf("split group tail: %v", err)
	}
	if got := countOriginMessages(t, p, convID, older); got != 2 {
		t.Fatalf("older public origins = %d, want 2", got)
	}
	if got := countOriginMessages(t, p, convID, tail); got != groupLCMFreshTail {
		t.Fatalf("tail public origins = %d, want %d", got, groupLCMFreshTail)
	}
}

func countOriginMessages(t *testing.T, p *Provider, convID string, items []sqlc.CtxItem) int {
	t.Helper()
	count := 0
	for _, item := range items {
		if item.ItemType != itemTypeMessage || !item.MessageID.Valid {
			continue
		}
		row, err := p.q.GetMessage(context.Background(), sqlc.GetMessageParams{
			ID:             item.MessageID.String,
			ConversationID: convID,
		})
		if err != nil {
			t.Fatalf("get message %s: %v", item.MessageID.String, err)
		}
		if row.OriginGroupMessageID.Valid {
			count++
		}
	}
	return count
}

// TestGroupRotationResetsLCMButKeepsWatermark pins both halves of group `/new`:
// the successor receives a fresh per-Agent LCM, while the group-level ingest
// cursor survives so public messages consumed by the predecessor are not replayed.
func TestGroupRotationResetsLCMButKeepsWatermark(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	first, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "group-rotation", ActorType: eventlog.ActorHuman,
		ActorID: "user-1", Content: "old-a", PlatformMessageID: "rotation-1",
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	second, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "group-rotation", ActorType: eventlog.ActorHuman,
		ActorID: "user-2", Content: "old-b", PlatformMessageID: "rotation-2",
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	before := memory.Session{
		ID: uuid.Must(uuid.NewV7()).String(), AgentID: "agent-a",
		UserID: first.GroupID, GroupID: first.GroupID,
	}
	if err := p.SyncGroupEventsBefore(ctx, before, second.Seq); err != nil {
		t.Fatalf("sync predecessor events: %v", err)
	}
	if err := p.AppendGroupTurn(
		ctx,
		before,
		second.Message.ID,
		ai.UserMessage{Content: "old-b"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "old-reply"}}},
	); err != nil {
		t.Fatalf("append predecessor turn: %v", err)
	}
	if err := p.CommitGroupCursor(ctx, before, second.Seq); err != nil {
		t.Fatalf("commit predecessor cursor: %v", err)
	}
	watermark := mustGroupCursor(t, p, first.GroupID, groupCursorPipeline("agent-a"))
	if watermark != second.Seq {
		t.Fatalf("predecessor watermark = %d, want %d", watermark, second.Seq)
	}

	after := memory.Session{
		ID: uuid.Must(uuid.NewV7()).String(), AgentID: "agent-a",
		UserID: first.GroupID, GroupID: first.GroupID,
	}
	rotateCtx := authz.WithAgentID(authz.WithGroupID(ctx, first.GroupID), "agent-a")
	if err := p.RotateInfo(rotateCtx, before.ID, memory.SessionInfo{
		ID: after.ID, AgentID: after.AgentID, UserID: after.UserID,
		GroupID: after.GroupID, Kind: "chat",
	}); err != nil {
		t.Fatalf("rotate group session: %v", err)
	}
	if got := mustGroupCursor(t, p, first.GroupID, groupCursorPipeline("agent-a")); got != watermark {
		t.Fatalf("rotation changed watermark to %d, want %d", got, watermark)
	}

	if _, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "group-rotation", ActorType: eventlog.ActorHuman,
		ActorID: "user-1", Content: "new-a", PlatformMessageID: "rotation-3",
	}); err != nil {
		t.Fatalf("append third event: %v", err)
	}
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "group-rotation", ActorType: eventlog.ActorHuman,
		ActorID: "user-2", Content: "new-b", PlatformMessageID: "rotation-4",
	})
	if err != nil {
		t.Fatalf("append trigger event: %v", err)
	}
	if err := p.SyncGroupEventsBefore(ctx, after, trigger.Seq); err != nil {
		t.Fatalf("sync successor events: %v", err)
	}
	messages, err := p.Assemble(ctx, after, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble successor: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("successor messages = %d, want one post-rotation event", len(messages))
	}
	if got := flattenUserMessage(messages[0].(ai.UserMessage)); got != fmt.Sprintf("[seq:%d user-1]: new-a", trigger.Seq-1) {
		t.Fatalf("successor message = %q, want only the post-rotation event", got)
	}
	if mustConversationID(t, p, before) == mustConversationID(t, p, after) {
		t.Fatal("rotation reused the predecessor conversation")
	}
}

func mustConversationID(t *testing.T, p *Provider, session memory.Session) string {
	t.Helper()
	convID, err := p.getOrCreateConversation(context.Background(), session)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	return convID
}

func assertRole(t *testing.T, msg ai.Message, expected string) {
	t.Helper()
	role := memory.MessageRole(msg)
	if role != expected {
		t.Fatalf("expected role %q, got %q", expected, role)
	}
}

func flattenUserMessage(um ai.UserMessage) string {
	switch c := um.Content.(type) {
	case []ai.ContentBlock:
		return ai.FlattenText(c)
	case string:
		return c
	default:
		return ""
	}
}
