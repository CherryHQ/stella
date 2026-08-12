package lcm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestAssembleContextWindowLoaderMixedLargeWindow(t *testing.T) {
	db := newAssemblerTestDB(t)
	defer db.Close()

	ctx := context.Background()
	q := sqlc.New(db)
	convID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind) VALUES ($1, $2, 'test', 'chat')`, convID, "sess-context-window-loader"); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}

	ordinal := int64(0)
	appendMessage := func(id string, seq int64, role, eventType, content string) {
		t.Helper()
		actorType := eventlog.ActorHuman
		if role != roleUser {
			actorType = eventlog.ActorAgent
		}
		if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
			ID:             id,
			ConversationID: convID,
			Seq:            seq,
			Role:           role,
			EventType:      eventType,
			Content:        content,
			ActorType:      string(actorType),
			TokenCount:     int64(memoryEstimate(content)),
		}); err != nil {
			t.Fatalf("create message %s: %v", id, err)
		}
		ordinal++
		if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
			ConversationID: convID,
			Ordinal:        ordinal,
			ItemType:       itemTypeMessage,
			MessageID:      pgtype.Text{String: id, Valid: true},
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
		appendMessage(uuid.NewString(), int64(i), roleUser, eventTypeText, fmt.Sprintf("message %03d", i))
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
		SummaryID:      pgtype.Text{String: "sum-child", Valid: true},
		Role:           "",
	}); err != nil {
		t.Fatalf("append summary item: %v", err)
	}
	for i := 61; i <= 105; i++ {
		appendMessage(uuid.NewString(), int64(i), roleUser, eventTypeText, fmt.Sprintf("message %03d", i))
	}

	call, _ := json.Marshal(toolCallEnvelope{ID: "call-loader", Tool: "bash", Args: json.RawMessage(`{"command":"true"}`)})
	appendMessage(uuid.NewString(), 106, roleAssistant, eventTypeToolCall, string(call))
	result, _ := json.Marshal(toolResultEnvelope{ID: "call-loader", Tool: "bash", Result: json.RawMessage(`"ok"`)})
	appendMessage(uuid.NewString(), 107, roleTool, eventTypeToolResult, string(result))

	counting := &queryCountingDB{Pool: db}
	got, err := newAssembler(sqlc.New(counting), nil).assemble(ctx, convID, 1_000_000, 0)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	// Only the final tool result is a parts candidate. Plain user and assistant
	// rows are excluded; the candidate uses one batch part query.
	if counting.queries != 3 {
		t.Fatalf("assemble issued %d queries, want 3", counting.queries)
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

func TestAssembleParallelToolCallsRemainOneAssistantTurn(t *testing.T) {
	tests := []struct {
		name       string
		freshTail  int
		appendNext bool
	}{
		{name: "fresh tail", freshTail: 1},
		{name: "older budget history", freshTail: 1, appendNext: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAssemblerTestDB(t)
			defer db.Close()

			ctx := context.Background()
			q := sqlc.New(db)
			convID := uuid.NewString()
			if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (id, session_id, channel, kind) VALUES ($1, $2, 'test', 'chat')`, convID, uuid.NewString()); err != nil {
				t.Fatalf("insert conversation: %v", err)
			}

			ordinal := int64(0)
			seq := int64(0)
			appendMessage := func(role, eventType, content string) {
				t.Helper()
				ordinal++
				seq++
				id := uuid.NewString()
				if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
					ID:             id,
					ConversationID: convID,
					Seq:            seq,
					Role:           role,
					EventType:      eventType,
					Content:        content,
					ActorType:      string(eventlog.ActorAgent),
					TokenCount:     int64(memoryEstimate(content)),
				}); err != nil {
					t.Fatalf("create message: %v", err)
				}
				if err := q.AppendContextItem(ctx, sqlc.AppendContextItemParams{
					ConversationID: convID,
					Ordinal:        ordinal,
					ItemType:       itemTypeMessage,
					MessageID:      pgtype.Text{String: id, Valid: true},
					EventType:      eventType,
					Role:           role,
				}); err != nil {
					t.Fatalf("append context item: %v", err)
				}
			}

			appendMessage(roleUser, eventTypeText, "look up both values")
			for _, call := range []toolCallEnvelope{
				{ID: "call-a", Tool: "search", Args: json.RawMessage(`{"query":"a"}`)},
				{ID: "call-b", Tool: "search", Args: json.RawMessage(`{"query":"b"}`)},
			} {
				data, err := json.Marshal(call)
				if err != nil {
					t.Fatalf("marshal tool call: %v", err)
				}
				appendMessage(roleAssistant, eventTypeToolCall, string(data))
			}
			for _, result := range []toolResultEnvelope{
				{ID: "call-a", Tool: "search", Result: json.RawMessage(`"result-a"`)},
				{ID: "call-b", Tool: "search", Result: json.RawMessage(`"result-b"`)},
			} {
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatalf("marshal tool result: %v", err)
				}
				appendMessage(roleTool, eventTypeToolResult, string(data))
			}
			appendMessage(roleAssistant, eventTypeText, "combined answer")
			if tt.appendNext {
				// This user turn makes the parallel-tool turn compete in the older
				// budget path instead of remaining in the protected fresh tail.
				appendMessage(roleUser, eventTypeText, "next turn")
			}

			got, err := newAssembler(q, nil).assemble(ctx, convID, 100_000, tt.freshTail)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if len(got) < 5 {
				t.Fatalf("assembled %d messages, want at least 5", len(got))
			}
			assistant, ok := got[1].(ai.AssistantMessage)
			if !ok {
				t.Fatalf("parallel tool turn = %T, want ai.AssistantMessage", got[1])
			}
			if len(assistant.Content) != 2 {
				t.Fatalf("parallel tool blocks = %d, want 2: %#v", len(assistant.Content), assistant.Content)
			}
			for i, wantID := range []string{"call-a", "call-b"} {
				call, ok := assistant.Content[i].(ai.ToolCall)
				if !ok || call.ID != wantID {
					t.Fatalf("tool call %d = %#v, want %s", i, assistant.Content[i], wantID)
				}
			}
			for i, wantID := range []string{"call-a", "call-b"} {
				result, ok := got[i+2].(ai.ToolResultMessage)
				if !ok || result.ToolCallID != wantID {
					t.Fatalf("tool result %d = %#v, want %s", i, got[i+2], wantID)
				}
			}
		})
	}
}

type queryCountingDB struct {
	*pgxpool.Pool
	queries int
}

func (db *queryCountingDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	db.queries++
	return db.Pool.Query(ctx, query, args...)
}

func memoryEstimate(s string) int {
	return (len(s) + 3) / 4
}
