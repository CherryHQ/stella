package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/agent/runner"
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
	userEvt := runner.UserMessageToRPCEvent("hello world")
	if err := eng.Ingest(ctx, sessionID, userEvt); err != nil {
		t.Fatalf("Ingest user: %v", err)
	}

	// Ingest an assistant message.
	assistantEvt := runner.AssistantMessageToRPCEvent("hi there")
	if err := eng.Ingest(ctx, sessionID, assistantEvt); err != nil {
		t.Fatalf("Ingest assistant: %v", err)
	}

	// Verify messages via Assemble.
	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result))
	}
	if result[0].Type != runner.RPCEventUserMessage {
		t.Errorf("event[0].Type = %q, want %q", result[0].Type, runner.RPCEventUserMessage)
	}
	if result[0].Summary != "hello world" {
		t.Errorf("event[0].Summary = %q, want %q", result[0].Summary, "hello world")
	}
	if result[1].Type != runner.RPCEventMessageUpdate {
		t.Errorf("event[1].Type = %q, want %q", result[1].Type, runner.RPCEventMessageUpdate)
	}
}

func TestIngest_ToolEvents(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-tool"

	// Ingest a tool call with ID.
	argsJSON, _ := json.Marshal(map[string]string{"cmd": "ls"})
	toolCallEvt := runner.RPCEvent{
		Type:   runner.RPCEventToolCall,
		ID:     "call_abc123",
		Tool:   "bash",
		Result: argsJSON,
	}
	if err := eng.Ingest(ctx, sessionID, toolCallEvt); err != nil {
		t.Fatalf("Ingest tool_call: %v", err)
	}

	// Ingest a tool result with error flag.
	resultJSON, _ := json.Marshal("file1.txt\nfile2.txt")
	toolResultEvt := runner.RPCEvent{
		Type:   runner.RPCEventToolResult,
		ID:     "call_abc123",
		Tool:   "bash",
		Result: resultJSON,
		Error:  "command failed",
	}
	if err := eng.Ingest(ctx, sessionID, toolResultEvt); err != nil {
		t.Fatalf("Ingest tool_result: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result))
	}

	// Verify tool call round-trip: ID, Tool, args preserved.
	tc := result[0]
	if tc.Type != runner.RPCEventToolCall {
		t.Errorf("event[0].Type = %q, want %q", tc.Type, runner.RPCEventToolCall)
	}
	if tc.ID != "call_abc123" {
		t.Errorf("event[0].ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Tool != "bash" {
		t.Errorf("event[0].Tool = %q, want %q", tc.Tool, "bash")
	}
	if string(tc.Result) != string(argsJSON) {
		t.Errorf("event[0].Result = %s, want %s", tc.Result, argsJSON)
	}

	// Verify tool result round-trip: ID, Tool, error preserved.
	tr := result[1]
	if tr.Type != runner.RPCEventToolResult {
		t.Errorf("event[1].Type = %q, want %q", tr.Type, runner.RPCEventToolResult)
	}
	if tr.ID != "call_abc123" {
		t.Errorf("event[1].ID = %q, want %q", tr.ID, "call_abc123")
	}
	if tr.Tool != "bash" {
		t.Errorf("event[1].Tool = %q, want %q", tr.Tool, "bash")
	}
	if tr.Error != "command failed" {
		t.Errorf("event[1].Error = %q, want %q", tr.Error, "command failed")
	}
	if string(tr.Result) != string(resultJSON) {
		t.Errorf("event[1].Result = %s, want %s", tr.Result, resultJSON)
	}
}

func TestIngest_MultimodalRoundTrip(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-multimodal"

	// Create a multimodal user message with text + image.
	blocks := []runner.ContentBlockJSON{
		{Kind: runner.BlockKindText, Text: "describe this image"},
		{Kind: runner.BlockKindImage, Data: "base64data", MimeType: "image/png"},
	}
	blocksJSON, _ := json.Marshal(blocks)
	evt := runner.RPCEvent{
		Type:    runner.RPCEventUserMessage,
		Summary: "describe this image",
		Content: blocksJSON,
	}
	if err := eng.Ingest(ctx, sessionID, evt); err != nil {
		t.Fatalf("Ingest multimodal: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result))
	}

	got := result[0]
	if got.Type != runner.RPCEventUserMessage {
		t.Errorf("Type = %q, want %q", got.Type, runner.RPCEventUserMessage)
	}
	if got.Summary != "describe this image" {
		t.Errorf("Summary = %q, want %q", got.Summary, "describe this image")
	}
	if len(got.Content) == 0 {
		t.Error("expected Content to be preserved for multimodal message")
	}

	// Verify the content blocks round-trip.
	var gotBlocks []runner.ContentBlockJSON
	if err := json.Unmarshal(got.Content, &gotBlocks); err != nil {
		t.Fatalf("unmarshal content blocks: %v", err)
	}
	if len(gotBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(gotBlocks))
	}
	if gotBlocks[0].Kind != runner.BlockKindText || gotBlocks[0].Text != "describe this image" {
		t.Errorf("block[0] = %+v, want text block", gotBlocks[0])
	}
	if gotBlocks[1].Kind != runner.BlockKindImage || gotBlocks[1].Data != "base64data" {
		t.Errorf("block[1] = %+v, want image block", gotBlocks[1])
	}
}

func TestIngest_SkipUnmappable(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-skip"

	// agent_end events should be skipped.
	evt := runner.RPCEvent{Type: runner.RPCEventAgentEnd}
	if err := eng.Ingest(ctx, sessionID, evt); err != nil {
		t.Fatalf("Ingest agent_end: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 events for unmappable type, got %d", len(result))
	}
}

func TestIngestBatch(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-batch"

	evts := []runner.RPCEvent{
		runner.UserMessageToRPCEvent("first"),
		runner.AssistantMessageToRPCEvent("second"),
		runner.UserMessageToRPCEvent("third"),
	}

	if err := eng.IngestBatch(ctx, sessionID, evts); err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result))
	}
}

func TestAssembleAfterIngest(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()
	sessionID := "sess-assemble"

	// Ingest several messages.
	for i := 0; i < 5; i++ {
		evt := runner.UserMessageToRPCEvent("message content here for testing")
		if err := eng.Ingest(ctx, sessionID, evt); err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
	}

	// Assemble with a large budget should return all.
	result, err := eng.Assemble(ctx, sessionID, 100000, 20)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 events, got %d", len(result))
	}

	// Assemble with a tiny budget should return at least fresh tail.
	result, err = eng.Assemble(ctx, sessionID, 1, 2)
	if err != nil {
		t.Fatalf("Assemble tiny budget: %v", err)
	}
	if len(result) < 2 {
		t.Errorf("expected at least 2 events (fresh tail), got %d", len(result))
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
		var evt runner.RPCEvent
		if i%2 == 0 {
			evt = runner.UserMessageToRPCEvent("user message content for compaction test")
		} else {
			evt = runner.AssistantMessageToRPCEvent("assistant response content for compaction")
		}
		if err := eng.Ingest(ctx, sessionID, evt); err != nil {
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
		t.Error("expected events from assembly after compaction")
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
		evt := runner.UserMessageToRPCEvent("some content to fill tokens for testing purposes")
		if err := eng.Ingest(ctx, sessionID, evt); err != nil {
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

func TestEventToMessage(t *testing.T) {
	tests := []struct {
		name          string
		evt           runner.RPCEvent
		wantRole      string
		wantEventType string
		wantContent   string
	}{
		{
			name:          "user text message",
			evt:           runner.RPCEvent{Type: runner.RPCEventUserMessage, Summary: "hello"},
			wantRole:      RoleUser,
			wantEventType: EventTypeText,
			wantContent:   "hello",
		},
		{
			name:          "user multimodal message",
			evt:           runner.RPCEvent{Type: runner.RPCEventUserMessage, Content: json.RawMessage(`[{"kind":"text","text":"hi"},{"kind":"image","data":"abc"}]`)},
			wantRole:      RoleUser,
			wantEventType: EventTypeMultimodal,
			wantContent:   `[{"kind":"text","text":"hi"},{"kind":"image","data":"abc"}]`,
		},
		{
			name:          "assistant message",
			evt:           runner.RPCEvent{Type: runner.RPCEventMessageUpdate, Summary: "response"},
			wantRole:      RoleAssistant,
			wantEventType: EventTypeText,
			wantContent:   "response",
		},
		{
			name:          "tool call with ID",
			evt:           runner.RPCEvent{Type: runner.RPCEventToolCall, ID: "call_123", Tool: "bash", Result: []byte(`{"cmd":"ls"}`)},
			wantRole:      RoleAssistant,
			wantEventType: EventTypeToolCall,
		},
		{
			name:          "tool result with error",
			evt:           runner.RPCEvent{Type: runner.RPCEventToolResult, ID: "call_123", Tool: "bash", Result: []byte(`"error output"`), Error: "command failed"},
			wantRole:      RoleTool,
			wantEventType: EventTypeToolResult,
		},
		{
			name:          "agent end skipped",
			evt:           runner.RPCEvent{Type: runner.RPCEventAgentEnd},
			wantRole:      "",
			wantEventType: "",
			wantContent:   "",
		},
		{
			name:          "tool call no args",
			evt:           runner.RPCEvent{Type: runner.RPCEventToolCall, Tool: "read"},
			wantRole:      RoleAssistant,
			wantEventType: EventTypeToolCall,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, eventType, content := eventToMessage(tt.evt)
			if role != tt.wantRole {
				t.Errorf("role = %q, want %q", role, tt.wantRole)
			}
			if eventType != tt.wantEventType {
				t.Errorf("eventType = %q, want %q", eventType, tt.wantEventType)
			}
			if tt.wantContent != "" && content != tt.wantContent {
				t.Errorf("content = %q, want %q", content, tt.wantContent)
			}

			// For tool_call events, verify the envelope contains the right fields.
			if tt.wantEventType == EventTypeToolCall && content != "" {
				var env toolCallEnvelope
				if err := json.Unmarshal([]byte(content), &env); err != nil {
					t.Fatalf("unmarshal tool call envelope: %v", err)
				}
				if env.ID != tt.evt.ID {
					t.Errorf("envelope ID = %q, want %q", env.ID, tt.evt.ID)
				}
				if env.Tool != tt.evt.Tool {
					t.Errorf("envelope Tool = %q, want %q", env.Tool, tt.evt.Tool)
				}
			}

			// For tool_result events, verify error flag is preserved.
			if tt.wantEventType == EventTypeToolResult && content != "" {
				var env toolResultEnvelope
				if err := json.Unmarshal([]byte(content), &env); err != nil {
					t.Fatalf("unmarshal tool result envelope: %v", err)
				}
				if env.Error != tt.evt.Error {
					t.Errorf("envelope Error = %q, want %q", env.Error, tt.evt.Error)
				}
				if env.ID != tt.evt.ID {
					t.Errorf("envelope ID = %q, want %q", env.ID, tt.evt.ID)
				}
				if env.Tool != tt.evt.Tool {
					t.Errorf("envelope Tool = %q, want %q", env.Tool, tt.evt.Tool)
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

	// Ingest mixed events.
	evts := []runner.RPCEvent{
		runner.UserMessageToRPCEvent("hello"),
		runner.AssistantMessageToRPCEvent("hi there"),
		{Type: runner.RPCEventToolCall, ID: "call_1", Tool: "bash", Result: json.RawMessage(`{"cmd":"ls"}`)},
		{Type: runner.RPCEventToolResult, ID: "call_1", Tool: "bash", Result: json.RawMessage(`"file1.txt"`), Error: ""},
	}
	if err := eng.IngestBatch(ctx, sessionID, evts); err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	// Load full history.
	loaded, err := eng.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 4 {
		t.Fatalf("expected 4 events, got %d", len(loaded))
	}

	// Verify types.
	if loaded[0].Type != runner.RPCEventUserMessage {
		t.Errorf("event[0].Type = %q, want %q", loaded[0].Type, runner.RPCEventUserMessage)
	}
	if loaded[1].Type != runner.RPCEventMessageUpdate {
		t.Errorf("event[1].Type = %q, want %q", loaded[1].Type, runner.RPCEventMessageUpdate)
	}
	if loaded[2].Type != runner.RPCEventToolCall {
		t.Errorf("event[2].Type = %q, want %q", loaded[2].Type, runner.RPCEventToolCall)
	}
	if loaded[2].ID != "call_1" {
		t.Errorf("event[2].ID = %q, want %q", loaded[2].ID, "call_1")
	}
	if loaded[3].Type != runner.RPCEventToolResult {
		t.Errorf("event[3].Type = %q, want %q", loaded[3].Type, runner.RPCEventToolResult)
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
		t.Errorf("expected nil for nonexistent session, got %d events", len(result))
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
		t.Errorf("expected 0 events, got %d", len(result))
	}
}

// Suppress unused import warning for database/sql.
var _ = sql.ErrNoRows
