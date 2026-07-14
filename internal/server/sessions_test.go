package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestToSessionResponse(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	info := agentsession.Info{
		ID:         "sess-1",
		Channel:    "telegram",
		Title:      "Hello",
		AgentID:    "agent-1",
		UserID:     "42",
		CreatedAt:  now,
		LastActive: now.Add(time.Hour),
		Archived:   true,
	}
	resp := sessionResponseFromInfo(info)
	if resp.ID != "sess-1" {
		t.Errorf("ID = %q", resp.ID)
	}
	if resp.Channel != "telegram" {
		t.Errorf("Channel = %q", resp.Channel)
	}
	if resp.UserID != "42" {
		t.Errorf("UserID = %q", resp.UserID)
	}
	if !resp.Archived {
		t.Error("Archived should be true")
	}
	if !resp.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v", resp.CreatedAt)
	}
}

func TestDecodeToolCallBlock_valid(t *testing.T) {
	content := `{"id":"call1","tool":"bash","args":{"command":"ls"}}`
	block := decodeToolCallBlock(content)
	if block["type"] != "tool_call" {
		t.Errorf("type = %v, want tool_call", block["type"])
	}
	if block["id"] != "call1" {
		t.Errorf("id = %v, want call1", block["id"])
	}
	if block["name"] != "bash" {
		t.Errorf("name = %v, want bash", block["name"])
	}
}

func TestDecodeToolCallBlock_invalid(t *testing.T) {
	block := decodeToolCallBlock("not json")
	if block["type"] != "tool_call" {
		t.Errorf("type = %v, want tool_call", block["type"])
	}
	if block["name"] != "unknown" {
		t.Errorf("name = %v, want unknown", block["name"])
	}
}

func TestSerializeUserRow(t *testing.T) {
	row := sessionaccess.Message{ID: "msg-u1", Role: "user", Content: "hello", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := serializeUserRow(row)
	if m["role"] != "user" {
		t.Errorf("role = %v", m["role"])
	}
	if m["content"] != "hello" {
		t.Errorf("content = %v", m["content"])
	}
	if m["id"] != "msg-u1" {
		t.Errorf("id = %v, want msg-u1", m["id"])
	}
}

func TestSerializeToolRow(t *testing.T) {
	env := map[string]any{"id": "c1", "tool": "bash", "result": "ok"}
	b, _ := json.Marshal(env)
	row := sessionaccess.Message{Role: "tool", Content: string(b)}
	m := serializeToolRow(row)
	if m["role"] != "tool" {
		t.Errorf("role = %v", m["role"])
	}
	if m["tool_name"] != "bash" {
		t.Errorf("tool_name = %v", m["tool_name"])
	}
}

func TestSerializeToolRow_invalidJSON(t *testing.T) {
	row := sessionaccess.Message{Role: "tool", Content: "bad json"}
	m := serializeToolRow(row)
	if m["content"] != "bad json" {
		t.Errorf("content = %v, want 'bad json'", m["content"])
	}
}

func TestSerializeDBMessages_mixed(t *testing.T) {
	toolCall, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "args": map[string]any{"command": "ls"}})
	toolResult, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "result": "output"})
	rows := []sessionaccess.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", EventType: "text", Content: "world"},
		{Role: "assistant", EventType: "tool_call", Content: string(toolCall)},
		{Role: "tool", Content: string(toolResult)},
		{Role: "unknown_role", Content: "skip"},
	}
	result := serializeDBMessages(rows)
	// user, assistant(text+tool_call merged into one), tool; unknown_role is skipped
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d: %v", len(result), result)
	}
	if result[0]["role"] != "user" {
		t.Errorf("first role = %v", result[0]["role"])
	}
}

func TestListMessagesByLogicalPageMatchesSerializedWindow(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)

	conv, err := q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID:         uuid.NewString(),
		SessionID:  "session-1",
		Channel:    "chat",
		Kind:       "chat",
		Archived:   false,
		LastActive: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	toolCall, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "args": map[string]any{"command": "ls"}})
	toolResult, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "result": "output"})
	rows := []sqlc.CreateMessageParams{
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 1, Role: "user", EventType: "text", Content: "u1"},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 2, Role: "assistant", EventType: "text", Content: "a1"},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 3, Role: "assistant", EventType: "tool_call", Content: string(toolCall)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 4, Role: "tool", EventType: "text", Content: string(toolResult)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 5, Role: "assistant", EventType: "text", Content: "a2"},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 6, Role: "assistant", EventType: "thinking", Content: "think"},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 7, Role: "user", EventType: "text", Content: "u2"},
	}
	for _, row := range rows {
		if _, err := q.CreateMessage(ctx, row); err != nil {
			t.Fatalf("CreateMessage %s: %v", row.ID, err)
		}
	}

	allRows, err := q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetMessagesByConversation: %v", err)
	}
	all := serializeDBMessages(testMessagesFromRows(allRows))

	pageRows, err := q.ListMessagesByLogicalPage(ctx, sqlc.ListMessagesByLogicalPageParams{
		ConversationID: conv.ID,
		Limit:          3,
		Offset:         1,
	})
	if err != nil {
		t.Fatalf("ListMessagesByLogicalPage: %v", err)
	}
	got := serializeDBMessages(testMessagesFromLogicalRows(pageRows))
	want := all[1:4]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logical page mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func testMessagesFromRows(rows []sqlc.CtxMessage) []sessionaccess.Message {
	out := make([]sessionaccess.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionaccess.Message{ID: row.ID, Seq: row.Seq, Role: row.Role, EventType: row.EventType, Content: row.Content, TokenCount: row.TokenCount, CreatedAt: row.CreatedAt})
	}
	return out
}

func testMessagesFromLogicalRows(rows []sqlc.ListMessagesByLogicalPageRow) []sessionaccess.Message {
	out := make([]sessionaccess.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionaccess.Message{ID: row.ID, Seq: row.Seq, Role: row.Role, EventType: row.EventType, Content: row.Content, TokenCount: row.TokenCount, CreatedAt: row.CreatedAt})
	}
	return out
}

func TestSerializeAssistantRows_text(t *testing.T) {
	rows := []sessionaccess.Message{
		{ID: "msg-a1", Role: "assistant", EventType: "text", Content: "hi"},
	}
	m, consumed := serializeAssistantRows(rows, 0)
	if consumed != 1 {
		t.Errorf("consumed = %d, want 1", consumed)
	}
	if m["role"] != "assistant" {
		t.Errorf("role = %v", m["role"])
	}
	if m["id"] != "msg-a1" {
		t.Errorf("id = %v, want msg-a1", m["id"])
	}
}

// Multiple consecutive assistant rows merge into one turn; the merged turn must
// carry the first row's ID so historical pagination produces stable React keys.
func TestSerializeAssistantRows_mergedFirstRowID(t *testing.T) {
	rows := []sessionaccess.Message{
		{ID: "msg-a1", Role: "assistant", EventType: "thinking", Content: "..."},
		{ID: "msg-a2", Role: "assistant", EventType: "tool_call", Content: `{"id":"c1","tool":"bash","args":{}}`},
		{ID: "msg-a3", Role: "assistant", EventType: "text", Content: "ok"},
	}
	m, consumed := serializeAssistantRows(rows, 0)
	if consumed != 3 {
		t.Errorf("consumed = %d, want 3", consumed)
	}
	if m["id"] != "msg-a1" {
		t.Errorf("id = %v, want msg-a1 (first row of merged turn)", m["id"])
	}
}
