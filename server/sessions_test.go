package server

import (
	"encoding/json"
	"testing"
	"time"

	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
)

func TestToSessionResponse(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	info := memory.SessionInfo{
		ID:         "sess-1",
		Channel:    "telegram",
		Title:      "Hello",
		AgentID:    "agent-1",
		UserID:     42,
		CreatedAt:  now,
		LastActive: now.Add(time.Hour),
		Archived:   true,
	}
	resp := toSessionResponse(info)
	if resp.ID != "sess-1" {
		t.Errorf("ID = %q", resp.ID)
	}
	if resp.Channel != "telegram" {
		t.Errorf("Channel = %q", resp.Channel)
	}
	if resp.UserID != 42 {
		t.Errorf("UserID = %d", resp.UserID)
	}
	if !resp.Archived {
		t.Error("Archived should be true")
	}
	if resp.CreatedAt != now.Format(time.RFC3339) {
		t.Errorf("CreatedAt = %q", resp.CreatedAt)
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
	row := sqlc.CtxMessage{Role: "user", Content: "hello", CreatedAt: "2026-01-01T00:00:00Z"}
	m := serializeUserRow(row)
	if m["role"] != "user" {
		t.Errorf("role = %v", m["role"])
	}
	if m["content"] != "hello" {
		t.Errorf("content = %v", m["content"])
	}
}

func TestSerializeToolRow(t *testing.T) {
	env := map[string]any{"id": "c1", "tool": "bash", "result": "ok"}
	b, _ := json.Marshal(env)
	row := sqlc.CtxMessage{Role: "tool", Content: string(b)}
	m := serializeToolRow(row)
	if m["role"] != "tool" {
		t.Errorf("role = %v", m["role"])
	}
	if m["tool_name"] != "bash" {
		t.Errorf("tool_name = %v", m["tool_name"])
	}
}

func TestSerializeToolRow_invalidJSON(t *testing.T) {
	row := sqlc.CtxMessage{Role: "tool", Content: "bad json"}
	m := serializeToolRow(row)
	if m["content"] != "bad json" {
		t.Errorf("content = %v, want 'bad json'", m["content"])
	}
}

func TestSerializeDBMessages_mixed(t *testing.T) {
	toolCall, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "args": map[string]any{"command": "ls"}})
	toolResult, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "result": "output"})
	rows := []sqlc.CtxMessage{
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

func TestSerializeAssistantRows_text(t *testing.T) {
	rows := []sqlc.CtxMessage{
		{Role: "assistant", EventType: "text", Content: "hi"},
	}
	m, consumed := serializeAssistantRows(rows, 0)
	if consumed != 1 {
		t.Errorf("consumed = %d, want 1", consumed)
	}
	if m["role"] != "assistant" {
		t.Errorf("role = %v", m["role"])
	}
}
