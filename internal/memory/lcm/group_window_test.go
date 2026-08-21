package lcm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func appendWindowMessage(t *testing.T, store *eventlog.Store, platformGroup string, actor eventlog.ActorType, actorID, name, content string) eventlog.AppendResult {
	t.Helper()
	result, err := store.AppendGroupMessage(context.Background(), eventlog.Message{
		Platform: "test", PlatformGroupID: platformGroup, ActorType: actor, ActorID: actorID,
		ActorDisplayName: name, Content: content,
	})
	if err != nil {
		t.Fatalf("append group message: %v", err)
	}
	return result
}

func windowTexts(messages []ai.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, memory.MessageText(message))
	}
	return out
}

func TestGroupWindowExcludesUndeliveredPeersButShowsOwnDeliveryState(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	first := appendWindowMessage(t, store, "window-delivery", eventlog.ActorHuman, "u1", "Before", "visible")
	peerPending, err := store.AppendToGroup(context.Background(), first.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-b", ActorDisplayName: "Bee", Content: "peer pending", DeliveryState: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	peerFailed, err := store.AppendToGroup(context.Background(), first.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-c", ActorDisplayName: "See", Content: "peer failed"})
	if err != nil {
		t.Fatal(err)
	}
	ownPending, err := store.AppendToGroup(context.Background(), first.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-a", ActorDisplayName: "Ada", Content: "own pending", DeliveryState: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	ownFailed, err := store.AppendToGroup(context.Background(), first.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-a", ActorDisplayName: "Ada", Content: "own failed"})
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	for _, row := range []struct {
		id, state string
	}{{peerFailed.Message.ID, "failed"}, {ownFailed.Message.ID, "failed"}} {
		if _, err := q.SetGroupMessageDeliveryState(context.Background(), sqlc.SetGroupMessageDeliveryStateParams{ID: row.id, DeliveryState: row.state}); err != nil {
			t.Fatal(err)
		}
	}
	trigger := appendWindowMessage(t, store, "window-delivery", eventlog.ActorHuman, "u2", "Trigger", "go")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := p.Assemble(groupCtx(trigger.Seq), groupSess("agent-a", first.GroupID), 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	if !strings.Contains(joined, "visible") || !strings.Contains(joined, "[seq:4 @Ada (you)]: own pending (sending)") || !strings.Contains(joined, "[seq:5 @Ada (you)]: own failed (delivery failed — peers never saw this)") {
		t.Fatalf("own delivery view = %q", joined)
	}
	if strings.Contains(joined, "peer pending") || strings.Contains(joined, "peer failed") {
		t.Fatalf("undelivered peer row entered context: %q", joined)
	}
	_ = peerPending
	_ = ownPending
}

func TestGroupWindowRendersOwnPublishedReplyWhenPrivateTrajectoryIsLarge(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	anchor := appendWindowMessage(t, store, "window-own-reply", eventlog.ActorHuman, "u1", "Alice", "question")
	published, err := store.AppendToGroup(context.Background(), anchor.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-a", ActorDisplayName: "Ada", Content: "published answer"})
	if err != nil {
		t.Fatal(err)
	}
	trigger := appendWindowMessage(t, store, "window-own-reply", eventlog.ActorHuman, "u2", "Bob", "next")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	session := groupSess("agent-a", anchor.GroupID)
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	turn := memory.DeferredGroupTurn{
		Session: session, TriggerSeq: anchor.Seq, OriginGroupMessageID: anchor.Message.ID, Complete: true,
		OwnRows: []ai.Message{
			ai.UserMessage{Content: "question"},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "read"}}},
			ai.ToolResultMessage{ToolCallID: "call-1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: strings.Repeat("private-only ", 10_000)}}},
		},
	}
	if err := p.CommitGroupTurn(groupCtx(anchor.Seq), sqlc.New(tx), turn); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	messages, err := p.Assemble(groupCtx(trigger.Seq), session, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	if !strings.Contains(joined, "[seq:2 @Ada (you)]: published answer") {
		t.Fatalf("published own reply missing: %q", joined)
	}
	if strings.Contains(joined, "private-only") || strings.Contains(joined, "call-1") {
		t.Fatalf("private trajectory entered prompt: %q", joined)
	}
	_ = published
}

func TestGroupWindowBoundedRowsAndOmissionMarker(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	var first eventlog.AppendResult
	for i := range groupWindowMaxRows + 1 {
		result := appendWindowMessage(t, store, "window-cap", eventlog.ActorHuman, "u", "User", fmt.Sprintf("message %03d", i))
		if i == 0 {
			first = result
		}
	}
	trigger := appendWindowMessage(t, store, "window-cap", eventlog.ActorHuman, "u", "User", "trigger")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := p.Assemble(groupCtx(trigger.Seq), groupSess("agent-a", first.GroupID), 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != groupWindowMaxRows+1 || memory.MessageText(messages[0]) != groupHistoryOmittedMarker {
		t.Fatalf("bounded window = %d rows, first = %q", len(messages), memory.MessageText(messages[0]))
	}
	if strings.Contains(strings.Join(windowTexts(messages), "\n"), "message 000") {
		t.Fatal("oldest row survived the row cap")
	}
}

func TestGroupWindowBudgetAndQuantizedEvictionAreStable(t *testing.T) {
	// A fixed stream remains byte-identical until an eviction boundary. Crossing
	// it removes exactly one complete 10k block, never a message-sized prefix.
	first := []groupWindowEvent{{line: "first", tokens: 6_000}, {line: "second", tokens: 6_000}}
	before := quantizeGroupWindow(append([]groupWindowEvent(nil), first...), 20_000)
	if got := strings.Join(groupWindowLines(before), "\n"); got != "first\nsecond" {
		t.Fatalf("stable prefix = %q", got)
	}
	after := quantizeGroupWindow(append(first, groupWindowEvent{line: "third", tokens: 6_000}), 20_000)
	if got := strings.Join(groupWindowLines(after), "\n"); got != "third" {
		t.Fatalf("evicted window = %q, want one surviving post-boundary block", got)
	}

	db := openTestDB(t)
	store := eventlog.NewStore(db)
	firstMessage := appendWindowMessage(t, store, "window-budget", eventlog.ActorHuman, "u", "User", strings.Repeat("x", 80))
	for range 20 {
		appendWindowMessage(t, store, "window-budget", eventlog.ActorHuman, "u", "User", strings.Repeat("x", 80))
	}
	trigger := appendWindowMessage(t, store, "window-budget", eventlog.ActorHuman, "u", "User", "trigger")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const budget = 100
	messages, err := p.Assemble(groupCtx(trigger.Seq), groupSess("agent-a", firstMessage.GroupID), budget, 20)
	if err != nil {
		t.Fatal(err)
	}
	used := 0
	for _, message := range messages {
		used += estimateMessageTokens(message)
	}
	if used > budget {
		t.Fatalf("assembled tokens = %d, budget = %d", used, budget)
	}
}

func TestGroupWindowMentionAnchorAndQuantizedEviction(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	mention := appendWindowMessage(t, store, "window-mention", eventlog.ActorHuman, "u1", "Alice", strings.Repeat("mention ", 30))
	for i := range 4 {
		appendWindowMessage(t, store, "window-mention", eventlog.ActorHuman, "u2", "Bob", fmt.Sprintf("recent %d", i))
	}
	trigger := appendWindowMessage(t, store, "window-mention", eventlog.ActorHuman, "u3", "Cara", "trigger")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	window, _, err := p.loadGroupWindow(memory.WithGroupWake(groupCtx(trigger.Seq), memory.GroupWake{MentionSeq: mention.Seq}), mention.GroupID, "agent-a", trigger.Seq, mention.Seq, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(window) == 0 || !strings.Contains(window[0].line, "mention") {
		t.Fatal("waking mention was omitted from bounded context")
	}
}

func TestGroupWindowShowsSilentToolNoteUntilAcceptedTurn(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ('agent-a', 'Ada', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO channel (id, type) VALUES ('silent-note', 'web')`); err != nil {
		t.Fatal(err)
	}
	store := eventlog.NewStore(db)
	anchor := appendWindowMessage(t, store, "window-silent", eventlog.ActorHuman, "u1", "Alice", "investigate")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	session := groupSess("agent-a", anchor.GroupID)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn := memory.DeferredGroupTurn{
		Session: session, TriggerSeq: anchor.Seq, OriginGroupMessageID: anchor.Message.ID, Complete: true,
		OwnRows: []ai.Message{
			ai.UserMessage{Content: "investigate"},
			ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "read_file"}}},
			ai.ToolResultMessage{ToolCallID: "call-1", ToolName: "read_file", Content: []ai.ContentBlock{ai.TextContent{Text: "private result"}}},
		},
	}
	if err := p.CommitGroupTurn(groupCtx(anchor.Seq), sqlc.New(tx), turn); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	insertWindowDispatch(t, db, "d15a0000-0000-0000-0000-000000000301", anchor, "silent", "")

	trigger := appendWindowMessage(t, store, "window-silent", eventlog.ActorHuman, "u2", "Bob", "what happened?")
	messages, err := p.Assemble(groupCtx(trigger.Seq), session, 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	wantNote := "[note: you ran tools (read_file) at seq 1 without replying]"
	if !strings.Contains(joined, wantNote) || strings.Contains(joined, "private result") {
		t.Fatalf("silent prompt = %q, want %q without private result", joined, wantNote)
	}

	accepted := appendWindowMessage(t, store, "window-silent", eventlog.ActorHuman, "u3", "Cara", "new task")
	insertWindowDispatch(t, db, "d15a0000-0000-0000-0000-000000000302", accepted, "completed", "accepted-result")
	afterAccepted := appendWindowMessage(t, store, "window-silent", eventlog.ActorHuman, "u4", "Dan", "continue")
	messages, err = p.Assemble(groupCtx(afterAccepted.Seq), session, 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(windowTexts(messages), "\n"), wantNote) {
		t.Fatal("silent tool note survived a later accepted turn")
	}
}

func insertWindowDispatch(t *testing.T, db sqlc.DBTX, id string, message eventlog.AppendResult, status, resultMessageID string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count,
  lease_until, next_attempt_at, last_error, result_message_id, trigger_seq, kind
) VALUES ($1, $2, $3, 'agent-a', 'silent-note', $4, 1, NULL, NULL, '', $5, $6, 'wake')`,
		id, message.Message.ID, message.GroupID, status, resultMessageID, message.Seq); err != nil {
		t.Fatal(err)
	}
}

func groupWindowLines(events []groupWindowEvent) []string {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, event.line)
	}
	return lines
}
