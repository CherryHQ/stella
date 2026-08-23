package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
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
	if resp.ActivityStatus != "idle" {
		t.Errorf("ActivityStatus = %q, want idle", resp.ActivityStatus)
	}
}

func TestSessionActivityStatusUsesTerminalResult(t *testing.T) {
	now := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	info := agentsession.Info{LastTurnCompletedAt: now, LastTurnResult: memory.SessionTurnSuccess}
	if got := sessionActivityStatus(info); got != "success" {
		t.Fatalf("unviewed completion status = %q, want success", got)
	}
	info.LastTurnResult = memory.SessionTurnError
	if got := sessionActivityStatus(info); got != "error" {
		t.Fatalf("failed completion status = %q, want error", got)
	}
	info.LastTurnResult = memory.SessionTurnCanceled
	if got := sessionActivityStatus(info); got != "idle" {
		t.Fatalf("canceled completion status = %q, want idle", got)
	}
	info.LastTurnResult = memory.SessionTurnSuccess
	info.LastViewedAt = now
	if got := sessionActivityStatus(info); got != "idle" {
		t.Fatalf("viewed completion status = %q, want idle", got)
	}
}

func TestDecodeToolCallBlock_valid(t *testing.T) {
	content := `{"id":"call1","tool":"bash","args":{"command":"ls"}}`
	block := decodeToolCallBlock(content)
	if block.Type != apitypes.SessionMessageBlockTypeToolCall {
		t.Errorf("type = %v, want tool_call", block.Type)
	}
	if block.Id == nil || *block.Id != "call1" {
		t.Errorf("id = %v, want call1", block.Id)
	}
	if block.Name == nil || *block.Name != "bash" {
		t.Errorf("name = %v, want bash", block.Name)
	}
}

func TestDecodeToolCallBlock_invalid(t *testing.T) {
	block := decodeToolCallBlock("not json")
	if block.Type != apitypes.SessionMessageBlockTypeToolCall {
		t.Errorf("type = %v, want tool_call", block.Type)
	}
	if block.Name == nil || *block.Name != "unknown" {
		t.Errorf("name = %v, want unknown", block.Name)
	}
}

func TestSerializeUserRow(t *testing.T) {
	row := sessionaccess.Message{ID: "msg-u1", Role: "user", Content: "hello", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := serializeUserRow("agent", "session", row)
	if m.Role != apitypes.SessionMessageRoleUser {
		t.Errorf("role = %v", m.Role)
	}
	if m.Content == nil || *m.Content != "hello" {
		t.Errorf("content = %v", m.Content)
	}
	if m.Id != "msg-u1" {
		t.Errorf("id = %v, want msg-u1", m.Id)
	}
}

func TestSerializeUserRowProjectsCanonicalPartsWithoutParentBaseline(t *testing.T) {
	marker := "[file: /user/assets/photo.png]"
	row := sessionaccess.Message{
		ID: "msg-u1", Role: "user", Content: "parent baseline", CreatedAt: time.Now().UTC(),
		Parts: []sessionaccess.MessagePart{
			{Type: "text", Text: "caption"},
			{Type: "text", Text: marker},
			{Type: "image", MediaID: "media-1", MimeType: "image/png"},
		},
	}
	message := serializeUserRow("agent", "session", row)
	if message.Content == nil || *message.Content != "caption\n"+marker {
		t.Fatalf("content = %v, want ordered visible text parts only", message.Content)
	}
	if message.Blocks == nil || len(*message.Blocks) != 3 {
		t.Fatalf("blocks = %#v, want caption, marker, and image", message.Blocks)
	}
	if block := (*message.Blocks)[0]; block.Type != apitypes.SessionMessageBlockTypeText || block.Text == nil || *block.Text != "caption" {
		t.Errorf("first block = %#v, want caption", block)
	}
	if block := (*message.Blocks)[1]; block.Type != apitypes.SessionMessageBlockTypeText || block.Text == nil || *block.Text != marker {
		t.Errorf("second block = %#v, want durable marker", block)
	}
	if block := (*message.Blocks)[2]; block.Type != apitypes.SessionMessageBlockTypeImage || block.MediaId == nil || *block.MediaId != "media-1" {
		t.Errorf("third block = %#v, want durable image", block)
	}
}

func TestSerializeUserRowKeepsNonImageFileMarker(t *testing.T) {
	marker := "[file: /user/assets/report.pdf]"
	message := serializeUserRow("agent", "session", sessionaccess.Message{
		Role: "user", Content: "parent baseline", Parts: []sessionaccess.MessagePart{{Type: "text", Text: marker}},
	})
	if message.Content == nil || *message.Content != marker {
		t.Fatalf("content = %v, want PDF marker", message.Content)
	}
	if message.Blocks == nil || len(*message.Blocks) != 1 || (*message.Blocks)[0].Text == nil || *(*message.Blocks)[0].Text != marker {
		t.Fatalf("blocks = %#v, want PDF marker preserved", message.Blocks)
	}
}

func TestSerializeUserRowUsesOneDeletedMediaFallback(t *testing.T) {
	message := serializeUserRow("agent", "session", sessionaccess.Message{
		Role: "user", Content: "parent baseline", Parts: []sessionaccess.MessagePart{{Type: "text", Text: "stable fallback"}},
	})
	if message.Content == nil || *message.Content != "stable fallback" {
		t.Fatalf("content = %v, want stable fallback without parent baseline", message.Content)
	}
	if message.Blocks == nil || len(*message.Blocks) != 1 || (*message.Blocks)[0].Text == nil || *(*message.Blocks)[0].Text != "stable fallback" {
		t.Fatalf("blocks = %#v, want one fallback text block", message.Blocks)
	}
}

func TestSerializeToolRowUsesPartsWithoutLosingEnvelopeMetadata(t *testing.T) {
	env := map[string]any{"id": "c1", "tool": "bash", "result": "parent baseline"}
	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	message := serializeToolRow("agent", "session", sessionaccess.Message{
		Role: "tool", Content: string(encoded),
		Parts: []sessionaccess.MessagePart{
			{Type: "text", Text: "visible output"},
			{Type: "image", MediaID: "media-1", MimeType: "image/png"},
		},
	})
	if message.Content == nil || *message.Content != "visible output" {
		t.Fatalf("content = %v, want visible part text only", message.Content)
	}
	if message.ToolCallId == nil || *message.ToolCallId != "c1" || message.ToolName == nil || *message.ToolName != "bash" {
		t.Errorf("tool pairing lost: %#v", message)
	}
	if message.Blocks == nil || len(*message.Blocks) != 2 {
		t.Fatalf("blocks = %#v, want text and image", message.Blocks)
	}
}

func TestSerializeToolRow(t *testing.T) {
	env := map[string]any{"id": "c1", "tool": "bash", "result": "ok"}
	b, _ := json.Marshal(env)
	row := sessionaccess.Message{Role: "tool", Content: string(b)}
	m := serializeToolRow("agent", "session", row)
	if m.Role != apitypes.SessionMessageRoleTool {
		t.Errorf("role = %v", m.Role)
	}
	if m.ToolName == nil || *m.ToolName != "bash" {
		t.Errorf("tool_name = %v", m.ToolName)
	}
}

func TestSerializeToolRowExportsTypedChildAuditOnly(t *testing.T) {
	env := map[string]any{
		"id": "outer", "tool": "code", "result": "ok",
		"child_calls": []map[string]any{{
			"id": "outer:1", "name": "bash", "is_error": true, "error_kind": "tool_error",
		}},
	}
	b, _ := json.Marshal(env)
	m := serializeToolRow("agent", "session", sessionaccess.Message{Role: "tool", Content: string(b)})
	if m.ChildCalls == nil || len(*m.ChildCalls) != 1 {
		t.Fatalf("child_calls = %#v", m.ChildCalls)
	}
	child := (*m.ChildCalls)[0]
	if child.Id != "outer:1" || child.Name != "bash" || !child.IsError || child.ErrorKind == nil || *child.ErrorKind != apitypes.SessionChildToolCallAuditErrorKindToolError {
		t.Fatalf("child call = %#v", child)
	}
}

func TestSerializeToolRow_invalidJSON(t *testing.T) {
	row := sessionaccess.Message{Role: "tool", Content: "bad json"}
	m := serializeToolRow("agent", "session", row)
	if m.Content == nil || *m.Content != "bad json" {
		t.Errorf("content = %v, want 'bad json'", m.Content)
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
	result := serializeDBMessages("agent", "session", rows)
	// user, assistant(text+tool_call merged into one), tool; unknown_role is skipped
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d: %v", len(result), result)
	}
	if result[0].Role != apitypes.SessionMessageRoleUser {
		t.Errorf("first role = %v", result[0].Role)
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
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 1, Role: "user", EventType: "text", Content: "u1", ActorType: string(eventlog.ActorHuman)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 2, Role: "assistant", EventType: "text", Content: "a1", ActorType: string(eventlog.ActorAgent)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 3, Role: "assistant", EventType: "tool_call", Content: string(toolCall), ActorType: string(eventlog.ActorAgent)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 4, Role: "tool", EventType: "text", Content: string(toolResult), ActorType: string(eventlog.ActorAgent)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 5, Role: "assistant", EventType: "text", Content: "a2", ActorType: string(eventlog.ActorAgent)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 6, Role: "assistant", EventType: "thinking", Content: "think", ActorType: string(eventlog.ActorAgent)},
		{ID: uuid.NewString(), ConversationID: conv.ID, Seq: 7, Role: "user", EventType: "text", Content: "u2", ActorType: string(eventlog.ActorHuman)},
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
	all := serializeDBMessages("agent", "session", testMessagesFromRows(allRows))

	pageRows, err := q.ListMessagesByLogicalPage(ctx, sqlc.ListMessagesByLogicalPageParams{
		ConversationID: conv.ID,
		Limit:          3,
		Offset:         1,
	})
	if err != nil {
		t.Fatalf("ListMessagesByLogicalPage: %v", err)
	}
	got := serializeDBMessages("agent", "session", testMessagesFromLogicalRows(pageRows))
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
		{ID: "msg-a1", Role: "assistant", EventType: "text", Content: "hi", ActorType: string(eventlog.ActorAgent), ActorID: "agent-1", SourceSessionID: "session-1"},
	}
	m, consumed := serializeAssistantRows(rows, 0)
	if consumed != 1 {
		t.Errorf("consumed = %d, want 1", consumed)
	}
	if m.Role != apitypes.SessionMessageRoleAssistant {
		t.Errorf("role = %v", m.Role)
	}
	if m.Id != "msg-a1" {
		t.Errorf("id = %v, want msg-a1", m.Id)
	}
	if m.ActorType != apitypes.SessionMessageActorTypeAgent || m.ActorId == nil || *m.ActorId != "agent-1" || m.SourceSessionId == nil || *m.SourceSessionId != "session-1" {
		t.Errorf("actor = %s/%v/%v", m.ActorType, m.ActorId, m.SourceSessionId)
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
	if m.Id != "msg-a1" {
		t.Errorf("id = %v, want msg-a1 (first row of merged turn)", m.Id)
	}
}

// The eval driver reads the split from this field, so the API has to forward
// what the row recorded, and only that: a row written before the split says
// nothing about why it failed, and the serializer must not fill in a guess.
func TestSerializeToolRowForwardsErrorKind(t *testing.T) {
	withKind, _ := json.Marshal(map[string]any{
		"id": "c1", "tool": "bash", "result": "no such file",
		"is_error": true, "error_kind": "command_nonzero",
	})
	m := serializeToolRow("agent", "session", sessionaccess.Message{Role: "tool", Content: string(withKind)})
	if m.ErrorKind == nil || *m.ErrorKind != apitypes.SessionMessageErrorKindCommandNonzero {
		t.Errorf("error_kind = %v, want command_nonzero", m.ErrorKind)
	}

	legacy, _ := json.Marshal(map[string]any{"id": "c1", "tool": "bash", "result": "boom", "is_error": true})
	m = serializeToolRow("agent", "session", sessionaccess.Message{Role: "tool", Content: string(legacy)})
	if m.ErrorKind != nil {
		t.Errorf("error_kind = %v on a pre-split row, want absent", *m.ErrorKind)
	}
}
