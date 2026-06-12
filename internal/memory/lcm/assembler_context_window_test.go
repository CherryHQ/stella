package lcm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestAssembleContextWindowLoaderMixedLargeWindow(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	q := sqlc.New(db)
	convID := "conv-context-window-loader"
	if _, err := db.ExecContext(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind) VALUES (?, ?, 'test', 'chat')`, convID, "sess-context-window-loader"); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	ordinal := int64(0)
	appendMessage := func(id string, seq int64, role, eventType, content string) {
		t.Helper()
		if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ID:             id,
			ConversationID: convID,
			Seq:            seq,
			Role:           role,
			EventType:      eventType,
			Content:        content,
			TokenCount:     int64(memoryEstimate(content)),
		}); err != nil {
			t.Fatalf("create message %s: %v", id, err)
		}
		ordinal++
		if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
			ConversationID: convID,
			Ordinal:        ordinal,
			ItemType:       itemTypeMessage,
			MessageID:      sql.NullString{String: id, Valid: true},
			EventType:      eventType,
			Role:           role,
		}); err != nil {
			t.Fatalf("append message item %s: %v", id, err)
		}
	}
	createSummary := func(id, content string) {
		t.Helper()
		if err := q.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ID:                      id,
			ConversationID:          convID,
			Kind:                    kindLeaf,
			Depth:                   0,
			Content:                 content,
			TokenCount:              int64(memoryEstimate(content)),
			DescendantCount:         1,
			DescendantTokenCount:    int64(memoryEstimate(content)),
			SourceMessageTokenCount: int64(memoryEstimate(content)),
		}); err != nil {
			t.Fatalf("create summary %s: %v", id, err)
		}
	}

	for i := 1; i <= 60; i++ {
		appendMessage(fmt.Sprintf("msg-%03d", i), int64(i), roleUser, eventTypeText, fmt.Sprintf("message %03d", i))
	}
	createSummary("sum-parent", "parent summary")
	createSummary("sum-child", "child summary")
	if err := q.LinkSummaryToParent(ctx, sqlc.LinkSummaryToParentParams{SummaryID: "sum-child", ParentSummaryID: "sum-parent", Ordinal: 0}); err != nil {
		t.Fatalf("link summary parent: %v", err)
	}
	ordinal++
	if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
		ConversationID: convID,
		Ordinal:        ordinal,
		ItemType:       itemTypeSummary,
		SummaryID:      sql.NullString{String: "sum-child", Valid: true},
		Role:           "",
	}); err != nil {
		t.Fatalf("append summary item: %v", err)
	}
	for i := 61; i <= 105; i++ {
		appendMessage(fmt.Sprintf("msg-%03d", i), int64(i), roleUser, eventTypeText, fmt.Sprintf("message %03d", i))
	}

	call, _ := json.Marshal(toolCallEnvelope{ID: "call-loader", Tool: "bash", Args: json.RawMessage(`{"command":"true"}`)})
	appendMessage("msg-tool-call", 106, roleAssistant, eventTypeToolCall, string(call))
	result, _ := json.Marshal(toolResultEnvelope{ID: "call-loader", Tool: "bash", Result: json.RawMessage(`"ok"`)})
	appendMessage("msg-tool-result", 107, roleTool, eventTypeToolResult, string(result))

	counting := &queryCountingDB{DB: db}
	got, err := newAssembler(sqlc.New(counting), nil).assemble(ctx, convID, 1_000_000, 0)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if counting.queries != 2 {
		t.Fatalf("assemble issued %d queries, want 2", counting.queries)
	}
	if len(got) != 108 {
		t.Fatalf("assembled messages = %d, want 108", len(got))
	}
	if text := got[0].(ai.UserMessage).Content.(string); text != "message 001" {
		t.Fatalf("first message = %q", text)
	}
	summary, ok := got[60].(ai.UserMessage)
	if !ok {
		t.Fatalf("summary slot = %T, want ai.UserMessage", got[60])
	}
	summaryXML, ok := summary.Content.(string)
	if !ok || !strings.Contains(summaryXML, `<summary_ref id="sum-parent" />`) || !strings.Contains(summaryXML, "child summary") {
		t.Fatalf("summary XML missing parent/content: %v", summary.Content)
	}
	assistant, ok := got[106].(ai.AssistantMessage)
	if !ok || len(assistant.Content) != 1 {
		t.Fatalf("tool call slot = %#v", got[106])
	}
	if call, ok := assistant.Content[0].(ai.ToolCall); !ok || call.ID != "call-loader" {
		t.Fatalf("tool call not reconstructed: %#v", assistant.Content[0])
	}
	toolResult, ok := got[107].(ai.ToolResultMessage)
	if !ok || toolResult.ToolCallID != "call-loader" || ai.FlattenText(toolResult.Content) != "ok" {
		t.Fatalf("tool result not reconstructed: %#v", got[107])
	}
}

type queryCountingDB struct {
	*sql.DB
	queries int
}

func (db *queryCountingDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	db.queries++
	return db.DB.QueryContext(ctx, query, args...)
}

func memoryEstimate(s string) int {
	return (len(s) + 3) / 4
}
