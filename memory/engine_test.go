package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/ai"
)

func testEngine(t *testing.T) Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "engine_test.db")
	summarizer := &StaticSummarizer{Response: "test summary"}
	eng, err := NewEngine(dbPath, summarizer, WithFreshTail(5))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestNewEngine(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "new_engine.db")
	summarizer := &StaticSummarizer{Response: "ok"}
	eng, err := NewEngine(dbPath, summarizer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewEngine_WithOptions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opts_engine.db")
	summarizer := &StaticSummarizer{Response: "ok"}
	eng, err := NewEngine(dbPath, summarizer, WithFreshTail(10), WithLogger(nil))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	e := eng.(*engine)
	if e.freshTail != 10 {
		t.Errorf("freshTail = %d, want 10", e.freshTail)
	}
}

func TestBootstrap(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// First bootstrap creates the conversation.
	if err := eng.Bootstrap(ctx, "sess-boot"); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Second bootstrap is idempotent.
	if err := eng.Bootstrap(ctx, "sess-boot"); err != nil {
		t.Fatalf("Bootstrap idempotent: %v", err)
	}
}

func TestIngest(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-ingest"

	// Ingest a user message.
	userMsg := ai.UserMessage{Content: "hello world"}
	if err := eng.Ingest(ctx, sessionID, userMsg); err != nil {
		t.Fatalf("Ingest user: %v", err)
	}

	// Ingest an assistant message.
	assistantMsg := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "hi there"}}}
	if err := eng.Ingest(ctx, sessionID, assistantMsg); err != nil {
		t.Fatalf("Ingest assistant: %v", err)
	}

	// Verify messages via Assemble.
	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	um, ok := result[0].(ai.UserMessage)
	if !ok {
		t.Errorf("result[0] type = %T, want ai.UserMessage", result[0])
	}
	if um.Content != "hello world" {
		t.Errorf("result[0] content = %v, want %q", um.Content, "hello world")
	}
	am, ok := result[1].(ai.AssistantMessage)
	if !ok {
		t.Errorf("result[1] type = %T, want ai.AssistantMessage", result[1])
	}
	if text := ai.FlattenText(am.Content); text != "hi there" {
		t.Errorf("result[1] text = %q, want %q", text, "hi there")
	}
}

func TestIngest_ToolEvents(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-tool"

	// Ingest a tool call (as part of an AssistantMessage).
	toolCallMsg := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{
				ID:        "call_abc123",
				Name:      "bash",
				Arguments: map[string]any{"cmd": "ls"},
			},
		},
	}
	if err := eng.Ingest(ctx, sessionID, toolCallMsg); err != nil {
		t.Fatalf("Ingest tool_call: %v", err)
	}

	// Ingest a tool result with error flag.
	toolResultMsg := ai.ToolResultMessage{
		ToolCallID: "call_abc123",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: "command failed"}},
		IsError:    true,
	}
	if err := eng.Ingest(ctx, sessionID, toolResultMsg); err != nil {
		t.Fatalf("Ingest tool_result: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	// Verify tool call round-trip: AssistantMessage with ToolCall block.
	am, ok := result[0].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("result[0] type = %T, want ai.AssistantMessage", result[0])
	}
	if len(am.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(am.Content))
	}
	tc, ok := am.Content[0].(ai.ToolCall)
	if !ok {
		t.Fatalf("content[0] type = %T, want ai.ToolCall", am.Content[0])
	}
	if tc.ID != "call_abc123" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Name != "bash" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "bash")
	}

	// Verify tool result round-trip.
	tr, ok := result[1].(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("result[1] type = %T, want ai.ToolResultMessage", result[1])
	}
	if tr.ToolCallID != "call_abc123" {
		t.Errorf("ToolCallID = %q, want %q", tr.ToolCallID, "call_abc123")
	}
	if tr.ToolName != "bash" {
		t.Errorf("ToolName = %q, want %q", tr.ToolName, "bash")
	}
	if !tr.IsError {
		t.Error("expected IsError = true")
	}
}

func TestIngest_MultimodalRoundTrip(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-multimodal"

	// Create a multimodal user message with text + image.
	multiMsg := ai.UserMessage{
		Content: []ai.ContentBlock{
			ai.TextContent{Text: "describe this image"},
			ai.ImageContent{Data: "base64data", MimeType: "image/png"},
		},
	}
	if err := eng.Ingest(ctx, sessionID, multiMsg); err != nil {
		t.Fatalf("Ingest multimodal: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}

	um, ok := result[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("result[0] type = %T, want ai.UserMessage", result[0])
	}

	blocks, ok := um.Content.([]ai.ContentBlock)
	if !ok {
		t.Fatalf("Content type = %T, want []ai.ContentBlock", um.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(blocks))
	}
	tc, ok := blocks[0].(ai.TextContent)
	if !ok || tc.Text != "describe this image" {
		t.Errorf("block[0] = %+v, want TextContent{Text: 'describe this image'}", blocks[0])
	}
	ic, ok := blocks[1].(ai.ImageContent)
	if !ok || ic.Data != "base64data" {
		t.Errorf("block[1] = %+v, want ImageContent{Data: 'base64data'}", blocks[1])
	}
}

func TestIngest_SkipUnmappable(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-skip"

	// nil message should be skipped (no ai.Message equivalent for agent_end).
	// Ingest with a nil message — messageToRows returns nil rows.
	if err := eng.Ingest(ctx, sessionID, nil); err != nil {
		t.Fatalf("Ingest nil: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 messages for nil input, got %d", len(result))
	}
}

func TestIngestBatch(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-batch"

	msgs := []ai.Message{
		ai.UserMessage{Content: "first"},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "second"}}},
		ai.UserMessage{Content: "third"},
	}

	if err := eng.IngestBatch(ctx, sessionID, msgs); err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
}

func TestAssembleAfterIngest(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-assemble"

	// Ingest several messages.
	for i := 0; i < 5; i++ {
		msg := ai.UserMessage{Content: "message content here for testing"}
		if err := eng.Ingest(ctx, sessionID, msg); err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
	}

	// Assemble with a large budget should return all.
	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 messages, got %d", len(result))
	}

	// Assemble with a tiny budget should return at least fresh tail.
	result, err = eng.Assemble(ctx, sessionID, 1, 2)
	if err != nil {
		t.Fatalf("Assemble tiny budget: %v", err)
	}
	if len(result) < 2 {
		t.Errorf("expected at least 2 messages (fresh tail), got %d", len(result))
	}
}

func TestCompactAfterIngest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "compact_test.db")
	summarizer := &StaticSummarizer{Response: "compacted summary of conversation"}
	eng, err := NewEngine(dbPath, summarizer, WithFreshTail(2))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	ctx := context.Background()
	sessionID := "sess-compact"

	// Ingest enough messages to trigger compaction (>= DefaultLeafChunkSize outside fresh tail).
	for i := 0; i < 15; i++ {
		var msg ai.Message
		if i%2 == 0 {
			msg = ai.UserMessage{Content: "user message content for compaction test"}
		} else {
			msg = ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "assistant response content for compaction"}}}
		}
		if err := eng.Ingest(ctx, sessionID, msg); err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
	}

	// Run compaction.
	result, compErr := eng.Compact(ctx, sessionID, CompactionIncremental)
	if compErr != nil {
		t.Fatalf("Compact: %v", compErr)
	}
	if result.LeafSummariesCreated == 0 {
		t.Error("expected at least 1 leaf summary created")
	}
	if result.MessagesCompacted == 0 {
		t.Error("expected messages to be compacted")
	}
	if result.TokensBefore == 0 {
		t.Error("expected non-zero tokens before")
	}

	// Assemble after compaction should still work.
	assembled, err := eng.Assemble(ctx, sessionID, 100000, 2)
	if err != nil {
		t.Fatalf("Assemble after compact: %v", err)
	}
	if len(assembled) == 0 {
		t.Error("expected messages from assembly after compaction")
	}
}

func TestEngine_NeedsCompaction(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-needs"

	// Empty conversation should not need compaction.
	if eng.NeedsCompaction(ctx, sessionID, 1000) {
		t.Error("empty conversation should not need compaction")
	}

	// Add some messages.
	for i := 0; i < 5; i++ {
		msg := ai.UserMessage{Content: "some content to fill tokens for testing purposes"}
		if err := eng.Ingest(ctx, sessionID, msg); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	// Low threshold should trigger.
	if !eng.NeedsCompaction(ctx, sessionID, 10) {
		t.Error("should need compaction with low threshold")
	}

	// Very high threshold should not trigger.
	if eng.NeedsCompaction(ctx, sessionID, 1000000) {
		t.Error("should not need compaction with very high threshold")
	}
}

func TestRetrieval(t *testing.T) {
	eng := testEngine(t)
	r := eng.Retrieval()
	if r == nil {
		t.Error("expected non-nil RetrievalEngine")
	}
}

func TestMessageToRows(t *testing.T) {
	tests := []struct {
		name          string
		msg           ai.Message
		wantLen       int
		wantRole      string
		wantEventType string
		wantContent   string
	}{
		{
			name:          "user text message",
			msg:           ai.UserMessage{Content: "hello"},
			wantLen:       1,
			wantRole:      RoleUser,
			wantEventType: EventTypeText,
			wantContent:   "hello",
		},
		{
			name: "user multimodal message",
			msg: ai.UserMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: "hi"},
				ai.ImageContent{Data: "abc"},
			}},
			wantLen:       1,
			wantRole:      RoleUser,
			wantEventType: EventTypeMultimodal,
		},
		{
			name:          "assistant text message",
			msg:           ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "response"}}},
			wantLen:       1,
			wantRole:      RoleAssistant,
			wantEventType: EventTypeText,
			wantContent:   "response",
		},
		{
			name: "assistant tool call",
			msg: ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{ID: "call_123", Name: "bash", Arguments: map[string]any{"cmd": "ls"}},
			}},
			wantLen:       1,
			wantRole:      RoleAssistant,
			wantEventType: EventTypeToolCall,
		},
		{
			name: "tool result with error",
			msg: ai.ToolResultMessage{
				ToolCallID: "call_123",
				ToolName:   "bash",
				Content:    []ai.ContentBlock{ai.TextContent{Text: "command failed"}},
				IsError:    true,
			},
			wantLen:       1,
			wantRole:      RoleTool,
			wantEventType: EventTypeToolResult,
		},
		{
			name:    "nil message skipped",
			msg:     nil,
			wantLen: 0,
		},
		{
			name: "assistant tool call no args",
			msg: ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.ToolCall{Name: "read"},
			}},
			wantLen:       1,
			wantRole:      RoleAssistant,
			wantEventType: EventTypeToolCall,
		},
		{
			name: "assistant text + tool call produces 2 rows",
			msg: ai.AssistantMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: "thinking..."},
				ai.ToolCall{ID: "call_456", Name: "bash", Arguments: map[string]any{"cmd": "pwd"}},
			}},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := messageToRows(tt.msg)
			if len(rows) != tt.wantLen {
				t.Fatalf("len(rows) = %d, want %d", len(rows), tt.wantLen)
			}
			if tt.wantLen == 0 {
				return
			}

			row := rows[0]
			if tt.wantRole != "" && row.role != tt.wantRole {
				t.Errorf("role = %q, want %q", row.role, tt.wantRole)
			}
			if tt.wantEventType != "" && row.eventType != tt.wantEventType {
				t.Errorf("eventType = %q, want %q", row.eventType, tt.wantEventType)
			}
			if tt.wantContent != "" && row.content != tt.wantContent {
				t.Errorf("content = %q, want %q", row.content, tt.wantContent)
			}

			// For tool_call rows, verify the envelope contains the right fields.
			if tt.wantEventType == EventTypeToolCall && row.content != "" {
				var env toolCallEnvelope
				if err := json.Unmarshal([]byte(row.content), &env); err != nil {
					t.Fatalf("unmarshal tool call envelope: %v", err)
				}
				// Verify envelope has expected tool name.
				if env.Tool == "" {
					t.Error("envelope Tool should not be empty")
				}
			}

			// For tool_result rows, verify error flag is preserved.
			if tt.wantEventType == EventTypeToolResult && row.content != "" {
				var env toolResultEnvelope
				if err := json.Unmarshal([]byte(row.content), &env); err != nil {
					t.Fatalf("unmarshal tool result envelope: %v", err)
				}
				if env.Error == "" && tt.name == "tool result with error" {
					t.Error("expected non-empty error in envelope")
				}
			}
		})
	}
}

func TestSaveInfo_LoadInfo(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info := SessionInfo{
		ID:      "sess-meta-1",
		Channel: "cli",
		Title:   "Test Session",
	}
	if err := eng.SaveInfo(ctx, info); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	loaded, err := eng.LoadInfo(ctx, "sess-meta-1")
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if loaded.ID != "sess-meta-1" {
		t.Errorf("ID = %q, want %q", loaded.ID, "sess-meta-1")
	}
	if loaded.Channel != "cli" {
		t.Errorf("Channel = %q, want %q", loaded.Channel, "cli")
	}
	if loaded.Title != "Test Session" {
		t.Errorf("Title = %q, want %q", loaded.Title, "Test Session")
	}
	if loaded.Archived {
		t.Error("expected Archived = false")
	}
}

func TestSaveInfo_Update(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Create.
	if err := eng.SaveInfo(ctx, SessionInfo{ID: "sess-upd", Channel: "cli", Title: "Original"}); err != nil {
		t.Fatalf("SaveInfo create: %v", err)
	}

	// Update title.
	if err := eng.SaveInfo(ctx, SessionInfo{ID: "sess-upd", Title: "Updated Title"}); err != nil {
		t.Fatalf("SaveInfo update: %v", err)
	}

	loaded, err := eng.LoadInfo(ctx, "sess-upd")
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if loaded.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", loaded.Title, "Updated Title")
	}
}

func TestSaveInfo_Archive(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	if err := eng.SaveInfo(ctx, SessionInfo{ID: "sess-arch", Title: "Archivable"}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	// Archive it.
	if err := eng.SaveInfo(ctx, SessionInfo{ID: "sess-arch", Title: "Archivable", Archived: true}); err != nil {
		t.Fatalf("SaveInfo archive: %v", err)
	}

	loaded, err := eng.LoadInfo(ctx, "sess-arch")
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if !loaded.Archived {
		t.Error("expected Archived = true")
	}
}

func TestListInfo(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Create sessions.
	for _, s := range []SessionInfo{
		{ID: "sess-a", Channel: "cli", Title: "A"},
		{ID: "sess-b", Channel: "telegram", Title: "B"},
		{ID: "sess-c", Channel: "cli", Title: "C", Archived: true},
	} {
		if err := eng.SaveInfo(ctx, s); err != nil {
			t.Fatalf("SaveInfo %s: %v", s.ID, err)
		}
	}

	// List without archived.
	active, err := eng.ListInfo(ctx, false)
	if err != nil {
		t.Fatalf("ListInfo active: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active sessions, got %d", len(active))
	}

	// List with archived.
	all, err := eng.ListInfo(ctx, true)
	if err != nil {
		t.Fatalf("ListInfo all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 total sessions, got %d", len(all))
	}
}

func TestLoadInfo_NotFound(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	_, err := eng.LoadInfo(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestLoad(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-load"

	// Ingest mixed messages.
	// Note: assistant text + tool call are separate AssistantMessages but
	// rowsToMessages merges consecutive assistant rows into one AssistantMessage.
	msgs := []ai.Message{
		ai.UserMessage{Content: "hello"},
		ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "hi there"},
			ai.ToolCall{ID: "call_1", Name: "bash", Arguments: map[string]any{"cmd": "ls"}},
		}},
		ai.ToolResultMessage{ToolCallID: "call_1", ToolName: "bash", Content: []ai.ContentBlock{ai.TextContent{Text: "file1.txt"}}},
	}
	if err := eng.IngestBatch(ctx, sessionID, msgs); err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	// Load full history.
	loaded, err := eng.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(loaded))
	}

	// Verify types.
	if _, ok := loaded[0].(ai.UserMessage); !ok {
		t.Errorf("loaded[0] type = %T, want ai.UserMessage", loaded[0])
	}
	am, ok := loaded[1].(ai.AssistantMessage)
	if !ok {
		t.Errorf("loaded[1] type = %T, want ai.AssistantMessage", loaded[1])
	} else {
		// Should contain text + tool call merged.
		if text := ai.FlattenText(am.Content); text != "hi there" {
			t.Errorf("loaded[1] text = %q, want %q", text, "hi there")
		}
		hasToolCall := false
		for _, b := range am.Content {
			if tc, ok := b.(ai.ToolCall); ok {
				hasToolCall = true
				if tc.ID != "call_1" {
					t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_1")
				}
			}
		}
		if !hasToolCall {
			t.Error("expected tool call in loaded[1]")
		}
	}
	if _, ok := loaded[2].(ai.ToolResultMessage); !ok {
		t.Errorf("loaded[2] type = %T, want ai.ToolResultMessage", loaded[2])
	}
}

func TestLoad_EmptySession(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	result, err := eng.Load(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for nonexistent session, got %d messages", len(result))
	}
}

func TestLoad_BootstrappedEmpty(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	if err := eng.Bootstrap(ctx, "sess-empty"); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	result, err := eng.Load(ctx, "sess-empty")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 messages, got %d", len(result))
	}
}

// Suppress unused import warning for database/sql.
var _ = sql.ErrNoRows
