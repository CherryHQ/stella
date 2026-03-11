package lcm

import (
	"context"
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

	// Ingest a tool call.
	argsJSON, _ := json.Marshal(map[string]string{"cmd": "ls"})
	toolCallEvt := runner.RPCEvent{
		Type:   runner.RPCEventToolCall,
		Tool:   "bash",
		Result: argsJSON,
	}
	if err := eng.Ingest(ctx, sessionID, toolCallEvt); err != nil {
		t.Fatalf("Ingest tool_call: %v", err)
	}

	// Ingest a tool result.
	resultJSON, _ := json.Marshal("file1.txt\nfile2.txt")
	toolResultEvt := runner.RPCEvent{
		Type:   runner.RPCEventToolResult,
		Result: resultJSON,
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

func TestEventToRoleContent(t *testing.T) {
	tests := []struct {
		name        string
		evt         runner.RPCEvent
		wantRole    string
		wantContent string
	}{
		{
			name:        "user message",
			evt:         runner.RPCEvent{Type: runner.RPCEventUserMessage, Summary: "hello"},
			wantRole:    RoleUser,
			wantContent: "hello",
		},
		{
			name:        "assistant message",
			evt:         runner.RPCEvent{Type: runner.RPCEventMessageUpdate, Summary: "response"},
			wantRole:    RoleAssistant,
			wantContent: "response",
		},
		{
			name:        "tool call",
			evt:         runner.RPCEvent{Type: runner.RPCEventToolCall, Tool: "bash", Result: []byte(`{"cmd":"ls"}`)},
			wantRole:    RoleAssistant,
			wantContent: `bash: {"cmd":"ls"}`,
		},
		{
			name:        "tool result",
			evt:         runner.RPCEvent{Type: runner.RPCEventToolResult, Result: []byte(`"output"`)},
			wantRole:    RoleTool,
			wantContent: `"output"`,
		},
		{
			name:        "agent end skipped",
			evt:         runner.RPCEvent{Type: runner.RPCEventAgentEnd},
			wantRole:    "",
			wantContent: "",
		},
		{
			name:        "tool call no result",
			evt:         runner.RPCEvent{Type: runner.RPCEventToolCall, Tool: "read"},
			wantRole:    RoleAssistant,
			wantContent: "read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, content := eventToRoleContent(tt.evt)
			if role != tt.wantRole {
				t.Errorf("role = %q, want %q", role, tt.wantRole)
			}
			if content != tt.wantContent {
				t.Errorf("content = %q, want %q", content, tt.wantContent)
			}
		})
	}
}
