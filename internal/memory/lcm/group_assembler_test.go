package lcm

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedGroupMessages(t *testing.T, db *sql.DB, groupID string) string {
	t.Helper()
	el := eventlog.NewStore(db)
	ctx := context.Background()

	msgs := []eventlog.Message{
		{Platform: "test", PlatformGroupID: groupID, ActorType: eventlog.ActorHuman, ActorID: "user1", Content: "hello everyone", PlatformMessageID: "m1"},
		{Platform: "test", PlatformGroupID: groupID, ActorType: eventlog.ActorHuman, ActorID: "user2", Content: "hey there", PlatformMessageID: "m2"},
	}

	var gid string
	for _, m := range msgs {
		res, err := el.AppendGroupMessage(ctx, m)
		if err != nil {
			t.Fatalf("seed append: %v", err)
		}
		gid = res.GroupID
	}

	// Agent response
	_, err := el.AppendToGroup(ctx, gid, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent,
		ActorID:   "agent-a",
		Content:   "hi, how can I help?",
	})
	if err != nil {
		t.Fatalf("seed agent response: %v", err)
	}

	// Another human message
	_, err = el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: groupID, ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "what is the weather?", PlatformMessageID: "m3",
	})
	if err != nil {
		t.Fatalf("seed final message: %v", err)
	}

	return gid
}

func TestGroupAssemble_BasicFlow(t *testing.T) {
	db := openTestDB(t)
	gid := seedGroupMessages(t, db, "g1")

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	sess := memory.Session{
		ID:      "agent-a:group:" + gid,
		AgentID: "agent-a",
		GroupID: gid,
	}

	msgs, err := p.Assemble(context.Background(), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}

	// First two should be UserMessages (from other humans)
	assertRole(t, msgs[0], "user")
	assertRole(t, msgs[1], "user")
	// Third is the agent's own response → AssistantMessage
	assertRole(t, msgs[2], "assistant")
	// Fourth is another human message
	assertRole(t, msgs[3], "user")

	// Agent's own message should not have actor attribution prefix
	am := msgs[2].(ai.AssistantMessage)
	text := ai.FlattenText(am.Content)
	if text != "hi, how can I help?" {
		t.Fatalf("agent message text = %q, want plain text without attribution", text)
	}
}

func TestGroupAssemble_OtherAgentAsUser(t *testing.T) {
	db := openTestDB(t)
	el := eventlog.NewStore(db)
	ctx := context.Background()

	res, err := el.AppendGroupMessage(ctx, eventlog.Message{
		Platform: "test", PlatformGroupID: "g2", ActorType: eventlog.ActorHuman,
		ActorID: "user1", Content: "hello", PlatformMessageID: "m1",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	gid := res.GroupID

	// Agent B responds
	_, err = el.AppendToGroup(ctx, gid, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "agent-b", Content: "I'm agent B",
	})
	if err != nil {
		t.Fatalf("append agent-b: %v", err)
	}

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Assemble from agent-a's perspective → agent-b's message should be UserMessage
	sess := memory.Session{ID: "agent-a:group:" + gid, AgentID: "agent-a", GroupID: gid}
	msgs, err := p.Assemble(ctx, sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	assertRole(t, msgs[0], "user") // human
	assertRole(t, msgs[1], "user") // agent-b from agent-a's perspective

	um := msgs[1].(ai.UserMessage)
	text := flattenUserMessage(um)
	if text == "" {
		t.Fatal("expected agent-b's message as user message with attribution")
	}
}

func TestGroupAssemble_TokenBudget(t *testing.T) {
	db := openTestDB(t)
	gid := seedGroupMessages(t, db, "g3")

	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sess := memory.Session{ID: "agent-a:group:" + gid, AgentID: "agent-a", GroupID: gid}

	// Very small budget: should trim older messages
	msgs, err := p.Assemble(context.Background(), sess, 20, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) >= 4 {
		t.Fatalf("expected fewer than 4 messages with tight budget, got %d", len(msgs))
	}
	if len(msgs) == 0 {
		t.Fatal("should return at least some messages")
	}
}

func TestGroupAssemble_EmptyGroup(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sess := memory.Session{ID: "agent-a:group:nonexistent", AgentID: "agent-a", GroupID: "nonexistent"}
	msgs, err := p.Assemble(context.Background(), sess, 100_000, 20)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for empty group, got %d", len(msgs))
	}
}

func TestGroupAppend_IsNoop(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sess := memory.Session{ID: "agent-a:group:g1", AgentID: "agent-a", GroupID: "g1"}
	err = p.Append(context.Background(), sess, ai.UserMessage{Content: "test"})
	if err != nil {
		t.Fatalf("group append should be no-op, got: %v", err)
	}
}

func TestGroupBootstrap_IsNoop(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sess := memory.Session{ID: "agent-a:group:g1", AgentID: "agent-a", GroupID: "g1"}
	err = p.Bootstrap(context.Background(), sess)
	if err != nil {
		t.Fatalf("group bootstrap should be no-op, got: %v", err)
	}
}

func TestGroupNeedsCompaction_AlwaysFalse(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sess := memory.Session{ID: "agent-a:group:g1", AgentID: "agent-a", GroupID: "g1"}
	if p.NeedsCompaction(context.Background(), sess, 1.0) {
		t.Fatal("group sessions should never need compaction")
	}
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
