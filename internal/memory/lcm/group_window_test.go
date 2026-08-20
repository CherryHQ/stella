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

func TestGroupWindowDeliveredOnlyAndMediaPlaceholder(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	first := appendWindowMessage(t, store, "window-delivery", eventlog.ActorHuman, "u1", "Before", "visible")
	pending, err := store.AppendToGroup(context.Background(), first.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-b", ActorDisplayName: "Bee", Content: "pending", DeliveryState: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.AppendToGroup(context.Background(), first.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-c", ActorDisplayName: "See", Content: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	if _, err := q.SetGroupMessageDeliveryState(context.Background(), sqlc.SetGroupMessageDeliveryStateParams{ID: failed.Message.ID, DeliveryState: "failed"}); err != nil {
		t.Fatal(err)
	}
	media := appendWindowMessage(t, store, "window-delivery", eventlog.ActorHuman, "u2", "Image user", "[image]")
	trigger := appendWindowMessage(t, store, "window-delivery", eventlog.ActorHuman, "u3", "Trigger", "go")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := groupCtx(trigger.Seq)
	messages, err := p.Assemble(ctx, groupSess("agent-a", first.GroupID), 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	if !strings.Contains(joined, "visible") || !strings.Contains(joined, "[image]") || !strings.Contains(joined, "Image user") {
		t.Fatalf("delivered text window = %q", joined)
	}
	if strings.Contains(joined, "pending") || strings.Contains(joined, "failed") {
		t.Fatalf("non-delivered row entered peer context: %q", joined)
	}
	if _, err := q.SetGroupMessageDeliveryState(context.Background(), sqlc.SetGroupMessageDeliveryStateParams{ID: pending.Message.ID, DeliveryState: "delivered"}); err != nil {
		t.Fatal(err)
	}
	messages, err = p.Assemble(ctx, groupSess("agent-a", first.GroupID), 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(windowTexts(messages), "\n")
	if strings.Count(joined, "pending") != 1 || strings.Index(joined, "pending") > strings.Index(joined, "[image]") {
		t.Fatalf("delivered transition did not appear exactly once in sequence: %q", joined)
	}
	_ = media
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

func TestGroupWindowInterleavesPrivateContinuationAndPersistsOnlyOwnRows(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	anchor := appendWindowMessage(t, store, "window-interleave", eventlog.ActorHuman, "u1", "Alice", "question")
	own, err := store.AppendToGroup(context.Background(), anchor.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-a", ActorDisplayName: "Ada", Content: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := store.AppendToGroup(context.Background(), anchor.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-b", ActorDisplayName: "Bee", Content: "peer update"})
	if err != nil {
		t.Fatal(err)
	}
	trigger := appendWindowMessage(t, store, "window-interleave", eventlog.ActorHuman, "u2", "Bob", "next")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess := groupSess("agent-a", anchor.GroupID)
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	turn := memory.DeferredGroupTurn{
		Session: sess, TriggerSeq: anchor.Seq, OriginGroupMessageID: anchor.Message.ID, Complete: true,
		OwnRows: []ai.Message{ai.UserMessage{Content: "question"}, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "private answer"}}}},
	}
	if err := p.CommitGroupTurn(context.Background(), sqlc.New(tx), turn); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A retry sees the trigger origin and advances only the monotonic cursor.
	tx, err = db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CommitGroupTurn(context.Background(), sqlc.New(tx), turn); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages, err := p.Assemble(groupCtx(trigger.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	if strings.Count(joined, "answer") != 1 || strings.Index(joined, "private answer") < strings.Index(joined, "question") || strings.Contains(joined, "[seq:2 @Ada]: answer") {
		t.Fatalf("interleaved context = %q", joined)
	}
	if strings.Index(joined, "private answer") > strings.Index(joined, "peer update") {
		t.Fatalf("continuation did not follow anchor: %q", joined)
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM ctx_message WHERE conversation_id = (SELECT id FROM ctx_conversation WHERE session_id = $1)`, sess.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("ctx_message copied public rows: got %d, want 2", count)
	}
	_ = own
	_ = peer
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
	window, _, err := p.loadGroupWindow(memory.WithGroupWake(groupCtx(trigger.Seq), memory.GroupWake{MentionSeq: mention.Seq}), mention.GroupID, "agent-a", trigger.Seq, mention.Seq, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(window) == 0 || !strings.Contains(window[0].line, "mention") {
		t.Fatal("waking mention was omitted from bounded context")
	}
	sample := []groupWindowEvent{{tokens: 6_000}, {tokens: 6_000}, {tokens: 6_000}, {tokens: 6_000}}
	first := quantizeGroupWindow(append([]groupWindowEvent(nil), sample...), 20_000)
	second := quantizeGroupWindow(append([]groupWindowEvent(nil), sample...), 20_000)
	if len(first) != 2 || len(first) != len(second) {
		t.Fatalf("quantized eviction is non-deterministic or not block-aligned: %d, %d", len(first), len(second))
	}
}

func TestGroupWindowKeepsPrivateTurnWhoseTriggerLeftTheWindow(t *testing.T) {
	db := openTestDB(t)
	store := eventlog.NewStore(db)
	anchor := appendWindowMessage(t, store, "window-evicted-origin", eventlog.ActorHuman, "u1", "Alice", "question")
	if _, err := store.AppendToGroup(context.Background(), anchor.GroupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-b", ActorDisplayName: "Bee", Content: "peer update"}); err != nil {
		t.Fatal(err)
	}
	trigger := appendWindowMessage(t, store, "window-evicted-origin", eventlog.ActorHuman, "u2", "Bob", "next")
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess := groupSess("agent-a", anchor.GroupID)
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	turn := memory.DeferredGroupTurn{
		Session: sess, TriggerSeq: anchor.Seq, OriginGroupMessageID: anchor.Message.ID, Complete: true,
		OwnRows: []ai.Message{ai.UserMessage{Content: "question"}, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "private answer"}}}},
	}
	if err := p.CommitGroupTurn(context.Background(), sqlc.New(tx), turn); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The trigger leaves the delivered window (retraction or eviction). The
	// agent's tool history must outlive the sliding public window via its
	// stored anchor, not vanish with the canonical row.
	if _, err := db.Exec(context.Background(), `UPDATE ctx_group_message SET delivery_state = 'failed' WHERE id = $1`, anchor.Message.ID); err != nil {
		t.Fatal(err)
	}
	messages, err := p.Assemble(groupCtx(trigger.Seq), sess, 100_000, 20)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(windowTexts(messages), "\n")
	if !strings.Contains(joined, "private answer") || !strings.Contains(joined, "question") {
		t.Fatalf("evicted-origin private turn missing: %q", joined)
	}
	if strings.Index(joined, "private answer") > strings.Index(joined, "peer update") {
		t.Fatalf("private head turn should precede the public window: %q", joined)
	}
}
