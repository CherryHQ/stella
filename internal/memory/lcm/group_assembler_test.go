package lcm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestGroupAssembleExcludesFailedPeerDelivery(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	visible, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "failed-peer", ActorType: eventlog.ActorHuman,
		ActorID: "user-1", Content: "visible context", PlatformMessageID: "failed-peer-1",
	})
	if err != nil {
		t.Fatalf("append visible message: %v", err)
	}
	failed, err := el.AppendToGroup(ctx, visible.GroupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "agent-b", Content: "delivery never reached the group",
	})
	if err != nil {
		t.Fatalf("append failed peer message: %v", err)
	}
	q := sqlc.New(db)
	if _, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: failed.Message.ID, DeliveryState: "failed"}); err != nil {
		t.Fatalf("mark peer delivery failed: %v", err)
	}
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "failed-peer", ActorType: eventlog.ActorHuman,
		ActorID: "user-2", Content: "trigger", PlatformMessageID: "failed-peer-3",
	})
	if err != nil {
		t.Fatalf("append trigger: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	msgs, err := p.Assemble(groupCtx(trigger.Seq), groupSess("agent-a", visible.GroupID), 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("injected messages = %d, want only the visible peer message", len(msgs))
	}
	if got := flattenUserMessage(msgs[0].(ai.UserMessage)); got != "[seq:1 user-1]: visible context" {
		t.Fatalf("injected text = %q", got)
	}
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
	msgs, err := p.Assemble(assembleCtx, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

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
	msgs2, err := p.Assemble(assembleCtx2, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble turn 2: %v", err)
	}

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
	msgs, err := p.Assemble(assembleCtx, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
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
	msgs2, err := p.Assemble(assembleCtx2, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble turn 2: %v", err)
	}

	// Should have: agent history [user:"hello", assistant:"hi"] + injected [agent-b].
	if len(msgs2) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs2))
	}
	assertRole(t, msgs2[0], "user")      // agent history: user "hello"
	assertRole(t, msgs2[1], "assistant") // agent history: assistant "hi"
	assertRole(t, msgs2[2], "user")      // injected: agent-b

	text := flattenUserMessage(msgs2[2].(ai.UserMessage))
	if text != "[seq:3 @agent-b]: I'm agent B" {
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
	if _, err := p.Assemble(assembleCtx, sess, 100_000, 20); err != nil {
		t.Fatalf("first assemble: %v", err)
	}
	for i := range 20 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("large retry filler %02d %s", i, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")}); err != nil {
			t.Fatalf("append filler %d: %v", i, err)
		}
	}
	if _, err := p.Assemble(assembleCtx, sess, 20, 20); err != nil {
		t.Fatalf("retry assemble: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM ctx_message WHERE role = 'user' AND content = $1`, "[seq:1 user1]: already persisted").Scan(&count); err != nil {
		t.Fatalf("count persisted injected: %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted injected count = %d, want 1", count)
	}
}

func TestFilterAlreadyPersistedInjectedBatchesLargeCandidateSet(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, map[string]any{})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	q := sqlc.New(db)
	ctx := context.Background()
	convID := uuid.NewString()
	if _, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{ID: convID, SessionID: "session-large", Channel: "test", Kind: "chat", LastActive: time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for i, content := range []string{"candidate-0000", "candidate-0500", "candidate-1001"} {
		if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{ID: uuid.NewString(), ConversationID: convID, Seq: int64(i + 1), Role: "user", EventType: "text", Content: content, ActorType: string(eventlog.ActorHuman)}); err != nil {
			t.Fatalf("create message %s: %v", content, err)
		}
	}
	injected := make([]ai.Message, 0, 1005)
	for i := range 1005 {
		injected = append(injected, ai.UserMessage{Content: fmt.Sprintf("candidate-%04d", i)})
	}

	filtered, err := p.filterAlreadyPersistedInjected(ctx, convID, injected)
	if err != nil {
		t.Fatalf("filter injected: %v", err)
	}
	if len(filtered) != 1002 {
		t.Fatalf("filtered len = %d, want 1002", len(filtered))
	}
	for _, msg := range filtered {
		content := msg.(ai.UserMessage).Content.(string)
		if content == "candidate-0000" || content == "candidate-0500" || content == "candidate-1001" {
			t.Fatalf("persisted content %q was not filtered", content)
		}
	}
	if got := filtered[0].(ai.UserMessage).Content.(string); got != "candidate-0001" {
		t.Fatalf("first filtered content = %q, want order preserved", got)
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
	msgs, err := p.Assemble(assembleCtx, sess, 20, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) >= 5 {
		t.Fatalf("expected fewer than 5 messages with tight budget, got %d", len(msgs))
	}
}

func TestGroupAssemble_BudgetDropsParallelToolTurnAtomically(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	res, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "group-parallel-budget", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "new injected context", PlatformMessageID: "injected-1",
	})
	if err != nil {
		t.Fatalf("seed injected context: %v", err)
	}
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "group-parallel-budget", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "trigger", PlatformMessageID: "trigger-2",
	})
	if err != nil {
		t.Fatalf("seed trigger: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	sess := groupSess("agent-a", res.GroupID)
	if err := p.Append(groupCtx(0), sess,
		ai.UserMessage{Content: "old tool turn"},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call-a", Name: "search"},
			ai.ToolCall{ID: "call-b", Name: "search"},
		}},
		ai.ToolResultMessage{ToolCallID: "call-a", ToolName: "search", Content: []ai.ContentBlock{ai.TextContent{Text: "result-a"}}},
		ai.ToolResultMessage{ToolCallID: "call-b", ToolName: "search", Content: []ai.ContentBlock{ai.TextContent{Text: "result-b"}}},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "old final answer"}}},
	); err != nil {
		t.Fatalf("append parallel tool turn: %v", err)
	}

	// The injected group message pushes the assembled history over this budget.
	// The older user/tool turn must disappear as a whole, never as surviving
	// results or a detached final assistant response.
	msgs, err := p.Assemble(groupCtx(trigger.Seq), sess, 19, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("budgeted group history = %#v, want only injected context", msgs)
	}
	user, ok := msgs[0].(ai.UserMessage)
	if !ok || !strings.Contains(flattenUserMessage(user), "new injected context") {
		t.Fatalf("remaining group message = %#v, want injected context", msgs[0])
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
	msgs, err := p.Assemble(assembleCtx, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
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
	msgs, err := p.Assemble(ctx, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
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

func TestGroupNeedsCompaction_AlwaysFalse(t *testing.T) {
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
		t.Fatal("group sessions should never need compaction")
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
	msgs, err := p.Assemble(groupCtx(res3.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble turn 1: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("turn 1: expected 2 injected, got %d", len(msgs))
	}

	if got := p.getGroupCursor(context.Background(), gid, groupCursorPipeline("agent-a")); got != 0 {
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
	if got := p.getGroupCursor(context.Background(), gid, groupCursorPipeline("agent-a")); got != res3.Seq {
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
	msgs2, err := p.Assemble(groupCtx(res4.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble turn 2: %v", err)
	}
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
	msgs, err := p.Assemble(groupCtx(res2.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("assemble messages = %d, want 1", len(msgs))
	}
	if got := p.getGroupCursor(context.Background(), res1.GroupID, groupCursorPipeline("agent-a")); got != 0 {
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
	retryBeforeCommit, err := p.Assemble(groupCtx(res2.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("retry assemble before commit: %v", err)
	}
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
	if got := p.getGroupCursor(context.Background(), res1.GroupID, groupCursorPipeline("agent-a")); got != res2.Seq {
		t.Fatalf("cursor after commit = %d, want %d", got, res2.Seq)
	}
	historyAfterCommit, err := p.assembler.assemble(context.Background(), mustConversationID(t, p, sess), 100_000, 20)
	if err != nil {
		t.Fatalf("assemble raw history after commit: %v", err)
	}
	if len(historyAfterCommit) != 2 {
		t.Fatalf("persisted messages after commit = %d, want 2", len(historyAfterCommit))
	}
	retryMsgs, err := p.Assemble(groupCtx(res2.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("retry assemble: %v", err)
	}
	if len(retryMsgs) != 2 {
		t.Fatalf("retry assemble messages = %d, want 2 without duplicate injected", len(retryMsgs))
	}
}

func TestAssemblerSetsPreTrimInjectedRows(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()
	first, err := el.AppendGroupMessage(ctx, eventlog.Message{Platform: "test", PlatformGroupID: "pre-trim", ActorType: eventlog.ActorHuman, ActorID: "user-1", Content: strings.Repeat("old peer words ", 40), PlatformMessageID: "pre-trim-1"})
	if err != nil {
		t.Fatalf("append peer: %v", err)
	}
	trigger, err := el.AppendGroupMessage(ctx, eventlog.Message{Platform: "test", PlatformGroupID: "pre-trim", ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "trigger", PlatformMessageID: "pre-trim-2"})
	if err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sink := memory.NewGroupTurnSink()
	msgs, err := p.Assemble(memory.WithGroupTurnSink(groupCtx(trigger.Seq), sink), groupSess("agent-a", first.GroupID), 1, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("trimmed prompt rows = %d, want 0", len(msgs))
	}
	injected := sink.Injected()
	if len(injected) != 1 || !strings.Contains(flattenUserMessage(injected[0].(ai.UserMessage)), "old peer words") {
		t.Fatalf("pre-trim injected rows = %#v", injected)
	}
}

func TestTxGroupCommitterUsesOuterTxAndSessionLock(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupID := newEmptyGroupState(t, p)
	sess := groupSess("agent-a", groupID)
	ctx := groupCtx(5)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin outer tx: %v", err)
	}
	qtx := sqlc.New(tx)
	turn := memory.DeferredGroupTurn{
		Session:          sess,
		InjectedPeerRows: []ai.Message{ai.UserMessage{Content: "peer"}},
		OwnRows:          []ai.Message{ai.UserMessage{Content: "trigger"}, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}}}},
		TriggerSeq:       5,
		Complete:         true,
	}
	if err := p.CommitGroupTurn(ctx, qtx, turn); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("commit through outer tx: %v", err)
	}
	// The provider must not commit the caller's tx. Rollback removes all turn
	// rows, proving there was no nested commit.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback outer tx: %v", err)
	}
	if stats, err := p.Stats(ctx, sess); err != nil || stats.MessageCount != 0 {
		t.Fatalf("rolled-back turn stats = %+v, %v", stats, err)
	}

	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin committed outer tx: %v", err)
	}
	if err := p.CommitGroupTurn(ctx, sqlc.New(tx), turn); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("commit through outer tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit outer tx: %v", err)
	}
	if stats, err := p.Stats(ctx, sess); err != nil || stats.MessageCount != 3 {
		t.Fatalf("committed turn stats = %+v, %v", stats, err)
	}
	if got := p.getGroupCursor(ctx, groupID, groupCursorPipeline("agent-a")); got != 5 {
		t.Fatalf("cursor = %d, want 5", got)
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
	msgs, err := p.Assemble(assembleCtx, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble with triggerSeq=0 should not error: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages with triggerSeq=0, got %d", len(msgs))
	}
}

// TestGroupAssemble_RotationResetsContextButKeepsWatermark pins the two halves
// of a group `/new`: the successor session starts with an empty context, and the
// ingest cursor — keyed by (group, pipeline) rather than by session — survives
// the rotation, so the messages the old session already consumed are not
// re-injected into the fresh one.
func TestGroupAssemble_RotationResetsContextButKeepsWatermark(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	res1, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "grot", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "old-a", PlatformMessageID: "r1",
	})
	if err != nil {
		t.Fatalf("seed r1: %v", err)
	}
	gid := res1.GroupID
	res2, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "grot", ActorType: eventlog.ActorHuman,
		ActorID: "user2", Content: "old-b", PlatformMessageID: "r2",
	})
	if err != nil {
		t.Fatalf("seed r2: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Turn 1 on the pre-rotation session.
	before := memory.Session{ID: uuid.Must(uuid.NewV7()).String(), AgentID: "agent-a", UserID: gid, GroupID: gid}
	if _, err := p.Assemble(groupCtx(res2.Seq), before, 100_000, 20); err != nil {
		t.Fatalf("assemble before rotation: %v", err)
	}
	if err := p.Append(groupCtx(res2.Seq), before,
		ai.UserMessage{Content: "old-b"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "old-reply"}}},
	); err != nil {
		t.Fatalf("append before rotation: %v", err)
	}
	if err := p.CommitGroupCursor(context.Background(), before, res2.Seq); err != nil {
		t.Fatalf("commit cursor: %v", err)
	}
	watermark := p.getGroupCursor(context.Background(), gid, groupCursorPipeline("agent-a"))
	if watermark != res2.Seq {
		t.Fatalf("watermark before rotation = %d, want %d", watermark, res2.Seq)
	}

	// `/new`: the binding moves onto a fresh session id, and the predecessor is
	// archived in the same step. Both halves are required —
	// idx_one_agent_group_chat admits exactly one active kind=chat row per
	// (agent, group), so a successor cannot simply appear beside its predecessor.
	after := memory.Session{ID: uuid.Must(uuid.NewV7()).String(), AgentID: "agent-a", UserID: gid, GroupID: gid}
	rotateCtx := authz.WithAgentID(authz.WithUserID(context.Background(), gid), "agent-a")
	if err := p.RotateInfo(rotateCtx, before.ID, memory.SessionInfo{
		ID: after.ID, AgentID: "agent-a", UserID: gid, GroupID: gid, Kind: "chat",
	}); err != nil {
		t.Fatalf("rotate group session: %v", err)
	}
	if got := p.getGroupCursor(context.Background(), gid, groupCursorPipeline("agent-a")); got != watermark {
		t.Fatalf("rotation moved the watermark: got %d, want %d", got, watermark)
	}

	// New traffic: seq3 lands between turns, seq4 triggers the next turn.
	if _, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "grot", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "new-a", PlatformMessageID: "r3",
	}); err != nil {
		t.Fatalf("seed r3: %v", err)
	}
	res4, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "grot", ActorType: eventlog.ActorHuman,
		ActorID: "user2", Content: "new-b", PlatformMessageID: "r4",
	})
	if err != nil {
		t.Fatalf("seed r4: %v", err)
	}

	msgs, err := p.Assemble(groupCtx(res4.Seq), after, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble after rotation: %v", err)
	}
	// Only seq3 is injected: the pre-rotation history is gone with the old
	// session, and seq1/seq2 stay below the preserved watermark.
	if len(msgs) != 1 {
		t.Fatalf("after rotation: expected 1 injected message, got %d", len(msgs))
	}
	text := flattenUserMessage(msgs[0].(ai.UserMessage))
	if text != fmt.Sprintf("[seq:%d user1]: new-a", res4.Seq-1) {
		t.Fatalf("injected text = %q, want the between-turn message only", text)
	}

	// The predecessor's conversation is untouched, so the reset is a rebind and
	// not a delete.
	if mustConversationID(t, p, before) == mustConversationID(t, p, after) {
		t.Fatal("rotation must not reuse the predecessor's conversation")
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
