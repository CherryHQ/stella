package lcm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestAssembleFreshTailKeepsToolHeavyTurn(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { _ = db.Close() }()
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := newPhase3Session("tool-heavy")
	for i := range 5 {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("simple turn %d", i)}); err != nil {
			t.Fatalf("append simple turn %d: %v", i, err)
		}
	}

	calls := make([]ai.ContentBlock, 0, 16)
	msgs := []ai.Message{ai.UserMessage{Content: "tool heavy user turn"}}
	for i := range 16 {
		calls = append(calls, ai.ToolCall{ID: fmt.Sprintf("call-%02d", i), Name: "tool", Arguments: map[string]any{"i": i}})
	}
	msgs = append(msgs, ai.AssistantMessage{Content: calls})
	for i := range 16 {
		msgs = append(msgs, ai.ToolResultMessage{
			ToolCallID: fmt.Sprintf("call-%02d", i),
			ToolName:   "tool",
			Content:    []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("heavy result %02d", i)}},
		})
	}
	if err := p.Append(ctx, sess, msgs...); err != nil {
		t.Fatalf("append heavy turn: %v", err)
	}

	got, err := p.Assemble(ctx, sess, 100_000, 6)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	text := messagesText(got)
	for i := range 16 {
		if !strings.Contains(text, fmt.Sprintf("heavy result %02d", i)) {
			t.Fatalf("missing tool result %02d in assembled tail:\n%s", i, text)
		}
	}
}

func TestAssembleTailTokenCapDemotesOldestTurn(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { _ = db.Close() }()
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := newPhase3Session("tail-cap")
	if err := p.Append(ctx, sess, ai.UserMessage{Content: "old " + strings.Repeat("x", 1200)}); err != nil {
		t.Fatalf("append old turn: %v", err)
	}
	if err := p.Append(ctx, sess,
		ai.UserMessage{Content: "new turn"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "kept-call", Name: "echo"}}},
		ai.ToolResultMessage{ToolCallID: "kept-call", ToolName: "echo", Content: []ai.ContentBlock{ai.TextContent{Text: "kept result"}}},
	); err != nil {
		t.Fatalf("append new turn: %v", err)
	}

	got, err := p.Assemble(ctx, sess, 100, 6)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	text := messagesText(got)
	if strings.Contains(text, strings.Repeat("x", 40)) {
		t.Fatalf("oldest over-cap turn was not demoted out of the budget:\n%s", text)
	}
	if !strings.Contains(text, "new turn") || !strings.Contains(text, "kept result") {
		t.Fatalf("newest turn did not remain intact after cap:\n%s", text)
	}
	if !hasToolCall(got, "kept-call") || !hasToolResult(got, "kept-call") {
		t.Fatalf("tool pair split after cap: %#v", got)
	}
}

func TestSplitFreshTailFewerThanDefaultTurnsKeepsAllItems(t *testing.T) {
	items := []sqlc.CtxItem{
		phase3Item(1, roleUser, eventTypeText),
		phase3Item(2, roleAssistant, eventTypeText),
		phase3Item(3, roleUser, eventTypeText),
		phase3Item(4, roleAssistant, eventTypeText),
		phase3Item(5, roleUser, eventTypeText),
	}
	tail, older := splitFreshTail(items, 6)
	if len(older) != 0 || len(tail) != len(items) {
		t.Fatalf("tail/older lengths = %d/%d, want %d/0", len(tail), len(older), len(items))
	}
}

func TestSplitFreshTailHardCapSingleGiantTurn(t *testing.T) {
	items := make([]sqlc.CtxItem, 0, maxFreshTailItems+11)
	items = append(items, phase3Item(1, roleUser, eventTypeText))
	for i := 2; i <= maxFreshTailItems+11; i++ {
		items = append(items, phase3Item(int64(i), roleAssistant, eventTypeText))
	}
	tail, older := splitFreshTail(items, 6)
	if len(older) == 0 {
		t.Fatal("expected hard cap to leave older items in a single giant turn")
	}
	if got := countMessageItems(tail); got != maxFreshTailItems {
		t.Fatalf("tail message count = %d, want hard cap %d", got, maxFreshTailItems)
	}
}

func TestCompactionProtectsLastSixTurns(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { _ = db.Close() }()
	p, err := New(db, func(context.Context, string) (string, error) { return "summary", nil }, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	defer func() { _ = p.Close() }()

	ctx := context.Background()
	sess := newPhase3Session("compact-protect")
	for i := 1; i <= 16; i++ {
		if err := p.Append(ctx, sess, ai.UserMessage{Content: fmt.Sprintf("turn %02d", i)}); err != nil {
			t.Fatalf("append turn %d: %v", i, err)
		}
	}
	compactor := memory.Compactor(p)
	if _, err := compactor.Compact(ctx, sess, memory.CompactionIncremental); err != nil {
		t.Fatalf("compact: %v", err)
	}

	convID := mustPhase3ConversationID(t, db, sess.ID)
	rows, err := db.QueryContext(ctx, `
		SELECT m.seq
		FROM ctx_item ci
		JOIN ctx_message m ON m.id = ci.message_id
		WHERE ci.conversation_id = ? AND ci.item_type = 'message'
		ORDER BY m.seq
	`, convID)
	if err != nil {
		t.Fatalf("query active messages: %v", err)
	}
	defer func() { _ = rows.Close() }()
	active := map[int]bool{}
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan active seq: %v", err)
		}
		active[seq] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for seq := 11; seq <= 16; seq++ {
		if !active[seq] {
			t.Fatalf("fresh turn seq %d was compacted; active seqs=%v", seq, active)
		}
	}

	var coveredFresh int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM ctx_summary_message sm
		JOIN ctx_message m ON m.id = sm.message_id
		WHERE m.conversation_id = ? AND m.seq >= 11
	`, convID).Scan(&coveredFresh); err != nil {
		t.Fatalf("query summary coverage: %v", err)
	}
	if coveredFresh != 0 {
		t.Fatalf("fresh turns were included in summary coverage: %d", coveredFresh)
	}
}

func newPhase3Session(suffix string) memory.Session {
	return memory.Session{ID: "phase3:" + suffix, AgentID: "test", UserID: "1", Channel: "test"}
}

func phase3Item(ord int64, role, eventType string) sqlc.CtxItem {
	return sqlc.CtxItem{
		ConversationID: "conv-phase3",
		Ordinal:        ord,
		ItemType:       itemTypeMessage,
		MessageID:      sql.NullString{String: fmt.Sprintf("msg-%d", ord), Valid: true},
		EventType:      eventType,
		Role:           role,
	}
}

func messagesText(msgs []ai.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		b.WriteString(memory.MessageText(msg))
		b.WriteByte('\n')
	}
	return b.String()
}

func hasToolCall(msgs []ai.Message, id string) bool {
	for _, msg := range msgs {
		am, ok := msg.(ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range am.Content {
			if call, ok := block.(ai.ToolCall); ok && call.ID == id {
				return true
			}
		}
	}
	return false
}

func hasToolResult(msgs []ai.Message, id string) bool {
	for _, msg := range msgs {
		result, ok := msg.(ai.ToolResultMessage)
		if ok && result.ToolCallID == id {
			return true
		}
	}
	return false
}

func mustPhase3ConversationID(t *testing.T, db *sql.DB, sessionID string) string {
	t.Helper()
	var convID string
	if err := db.QueryRow(`SELECT id FROM ctx_conversation WHERE session_id = ?`, sessionID).Scan(&convID); err != nil {
		t.Fatalf("get conversation id: %v", err)
	}
	return convID
}
