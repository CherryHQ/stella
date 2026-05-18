package lcm

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestToInt(t *testing.T) {
	tests := []struct {
		input any
		want  int
		ok    bool
	}{
		{float64(5.0), 5, true},
		{int(3), 3, true},
		{int64(7), 7, true},
		{"string", 0, false},
		{nil, 0, false},
	}
	for _, tc := range tests {
		got, ok := toInt(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Errorf("toInt(%v) = (%d, %v), want (%d, %v)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		input string
		empty bool
	}{
		{"2024-01-15 10:30:00", false},
		{"2024-01-15T10:30:00Z", false},
		{"2024-01-15T10:30:00+00:00", false},
		{"invalid", true},
		{"", true},
	}
	for _, tc := range tests {
		got := parseTime(tc.input)
		if tc.empty && !got.IsZero() {
			t.Errorf("parseTime(%q): expected zero time, got %v", tc.input, got)
		}
		if !tc.empty && got.IsZero() {
			t.Errorf("parseTime(%q): expected non-zero time", tc.input)
		}
	}
}

func TestParseNullTime(t *testing.T) {
	// Invalid null string.
	ns := sql.NullString{Valid: false, String: ""}
	if parseNullTime(ns) != nil {
		t.Error("expected nil for invalid null string")
	}

	// Valid but unparseable.
	ns = sql.NullString{Valid: true, String: "notadate"}
	if parseNullTime(ns) != nil {
		t.Error("expected nil for unparseable date")
	}

	// Valid and parseable.
	ns = sql.NullString{Valid: true, String: "2024-01-15 10:30:00"}
	got := parseNullTime(ns)
	if got == nil {
		t.Error("expected non-nil time for valid date")
	}
}

func TestGenerateSummaryID(t *testing.T) {
	id1 := generateSummaryID()
	id2 := generateSummaryID()

	if !strings.HasPrefix(id1, "sum_") {
		t.Errorf("expected 'sum_' prefix, got %q", id1)
	}
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	// "sum_" + 16 hex chars = 20 chars.
	if len(id1) != 20 {
		t.Errorf("expected 20 char ID, got %d: %q", len(id1), id1)
	}
}

func TestParseNullTime_EmptyString(t *testing.T) {
	ns := sql.NullString{Valid: true, String: ""}
	if parseNullTime(ns) != nil {
		t.Error("expected nil for empty valid string")
	}
}

func TestFormatSummaryXML_Leaf(t *testing.T) {
	sum := sqlc.CtxSummary{
		ID:      "sum-1",
		Kind:    "leaf",
		Depth:   0,
		Content: "summary content here",
	}
	got := FormatSummaryXML(sum, nil)
	if !strings.Contains(got, `id="sum-1"`) {
		t.Errorf("expected id attribute, got %q", got)
	}
	if !strings.Contains(got, "summary content here") {
		t.Errorf("expected content, got %q", got)
	}
	if !strings.Contains(got, `<summary`) && !strings.Contains(got, `</summary>`) {
		t.Errorf("expected XML tags, got %q", got)
	}
}

func TestFormatSummaryXML_Condensed(t *testing.T) {
	sum := sqlc.CtxSummary{
		ID:              "sum-2",
		Kind:            kindCondensed,
		Depth:           1,
		Content:         "condensed content",
		DescendantCount: 5,
		EarliestAt:      sql.NullString{Valid: true, String: "2024-01-01 00:00:00"},
		LatestAt:        sql.NullString{Valid: true, String: "2024-01-02 00:00:00"},
	}
	parent := sqlc.CtxSummary{ID: "parent-1"}
	got := FormatSummaryXML(sum, []sqlc.CtxSummary{parent})

	if !strings.Contains(got, `descendant_count="5"`) {
		t.Errorf("expected descendant_count, got %q", got)
	}
	if !strings.Contains(got, `earliest_at=`) {
		t.Errorf("expected earliest_at, got %q", got)
	}
	if !strings.Contains(got, `<summary_ref id="parent-1"`) {
		t.Errorf("expected parent ref, got %q", got)
	}
}

func TestFormatSummaryXML_ContentWithNewline(t *testing.T) {
	sum := sqlc.CtxSummary{
		ID:      "sum-3",
		Kind:    "leaf",
		Content: "content with newline\n",
	}
	got := FormatSummaryXML(sum, nil)
	// Content already ends with \n, should not add another.
	if strings.Contains(got, "newline\n\n") {
		t.Errorf("should not double-add newline, got %q", got)
	}
}

func TestTruncateUTF8_NoTruncation(t *testing.T) {
	got := truncateUTF8("hello", 10)
	if got != "hello" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestTruncateUTF8_Truncation(t *testing.T) {
	got := truncateUTF8("hello world", 5)
	if got != "hello..." {
		t.Errorf("expected 'hello...', got %q", got)
	}
}

func TestTruncateUTF8_UTF8(t *testing.T) {
	got := truncateUTF8("日本語テスト", 3)
	if got != "日本語..." {
		t.Errorf("expected '日本語...', got %q", got)
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Error("expected boolToInt(true)=1")
	}
	if boolToInt(false) != 0 {
		t.Error("expected boolToInt(false)=0")
	}
}

// Test that parseTime handles all three layouts.
func TestParseTime_AllLayouts(t *testing.T) {
	cases := []struct {
		input  string
		layout string
	}{
		{"2024-03-15 12:00:00", "SQLite format"},
		{"2024-03-15T12:00:00Z", "ISO8601"},
		{time.Now().UTC().Format(time.RFC3339), "RFC3339"},
	}
	for _, tc := range cases {
		got := parseTime(tc.input)
		if got.IsZero() {
			t.Errorf("parseTime(%q) [%s]: expected non-zero time", tc.input, tc.layout)
		}
	}
}

func TestFormatMessageForSummarizer(t *testing.T) {
	// tool_result — normal result.
	env := toolResultEnvelope{ID: "tc1", Tool: "read_file", Result: json.RawMessage(`"file content here"`)}
	data, _ := json.Marshal(env)
	msg := sqlc.CtxMessage{Role: "tool", EventType: eventTypeToolResult, Content: string(data)}
	got := formatMessageForSummarizer(msg)
	want := "[tool:read_file] result(17 chars): file content here"
	if got != want {
		t.Errorf("tool_result normal: got %q, want %q", got, want)
	}

	// tool_result — error result.
	env = toolResultEnvelope{ID: "tc1", Tool: "read_file", Error: "file not found"}
	data, _ = json.Marshal(env)
	msg = sqlc.CtxMessage{Role: "tool", EventType: eventTypeToolResult, Content: string(data)}
	got = formatMessageForSummarizer(msg)
	want = "[tool:read_file] error: file not found"
	if got != want {
		t.Errorf("tool_result error: got %q, want %q", got, want)
	}

	// tool_call.
	tcEnv := toolCallEnvelope{ID: "tc1", Tool: "bash", Args: json.RawMessage(`{"command":"ls"}`)}
	data, _ = json.Marshal(tcEnv)
	msg = sqlc.CtxMessage{Role: "assistant", EventType: eventTypeToolCall, Content: string(data)}
	got = formatMessageForSummarizer(msg)
	want = `[assistant:call bash] args: {"command":"ls"}`
	if got != want {
		t.Errorf("tool_call: got %q, want %q", got, want)
	}

	// tool_call — large args are truncated.
	largeArgs := `"` + strings.Repeat("x", 400) + `"`
	tcEnv = toolCallEnvelope{ID: "tc2", Tool: "write_file", Args: json.RawMessage(largeArgs)}
	data, _ = json.Marshal(tcEnv)
	msg = sqlc.CtxMessage{Role: "assistant", EventType: eventTypeToolCall, Content: string(data)}
	got = formatMessageForSummarizer(msg)
	if len([]rune(got)) > len("[assistant:call write_file] args: ")+300+3 {
		t.Errorf("tool_call large args not truncated: len=%d", len(got))
	}
	if !strings.Contains(got, "[assistant:call write_file] args: ") {
		t.Errorf("tool_call prefix missing: %q", got)
	}

	// Fallback — malformed JSON.
	msg = sqlc.CtxMessage{Role: "tool", EventType: eventTypeToolResult, Content: "not json"}
	got = formatMessageForSummarizer(msg)
	want = "[tool] not json"
	if got != want {
		t.Errorf("fallback: got %q, want %q", got, want)
	}

	// Default — text message.
	msg = sqlc.CtxMessage{Role: "user", EventType: eventTypeText, Content: "hello"}
	got = formatMessageForSummarizer(msg)
	want = "[user] hello"
	if got != want {
		t.Errorf("default: got %q, want %q", got, want)
	}
}

func TestCompactOversizedTailResults(t *testing.T) {
	largeText := strings.Repeat("a", 10000) // ~2500 tokens
	largeTool := ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "read_file",
		Content:    []ai.ContentBlock{ai.TextContent{Text: largeText}},
	}
	largeTool2 := ai.ToolResultMessage{
		ToolCallID: "tc2",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: largeText}},
	}
	smallTool := ai.ToolResultMessage{
		ToolCallID: "tc3",
		ToolName:   "stat",
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
	}
	assistant := ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
	}
	user := ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "hi"}}}
	user2 := ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "follow-up"}}}

	// Large tool result from a completed prior turn — should be compacted.
	// Pattern: [largeTool, assistant(final), user(new turn)]
	msgs := []ai.Message{largeTool, assistant, user2}
	got, n := compactOversizedTailResults(msgs)
	if n != 1 {
		t.Errorf("expected 1 compacted, got %d", n)
	}
	tr, ok := got[0].(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("expected ToolResultMessage at 0, got %T", got[0])
	}
	if tr.ToolCallID != "tc1" || tr.ToolName != "read_file" {
		t.Errorf("metadata lost: got %+v", tr)
	}
	if strings.Contains(ai.FlattenText(tr.Content), "aaaaaaaa") {
		t.Error("large content should have been replaced")
	}
	if !strings.Contains(ai.FlattenText(tr.Content), "Content omitted") {
		t.Errorf("expected placeholder text, got %q", ai.FlattenText(tr.Content))
	}

	// Multi-step tool chain within the current user turn — all preserved.
	// Pattern: [user, asst(tc1), largeTool1, asst(tc2), largeTool2]
	// Assembling for the final answer: model still needs both tool results.
	msgs = []ai.Message{user, assistant, largeTool, assistant, largeTool2}
	got, n = compactOversizedTailResults(msgs)
	if n != 0 {
		t.Errorf("expected 0 compacted for in-flight multi-step chain, got %d", n)
	}
	if ai.FlattenText(got[2].(ai.ToolResultMessage).Content) != largeText {
		t.Error("first tool result in current turn must not be compacted")
	}
	if ai.FlattenText(got[4].(ai.ToolResultMessage).Content) != largeText {
		t.Error("second tool result in current turn must not be compacted")
	}

	// Small tool result from completed turn — preserved (under threshold).
	msgs = []ai.Message{smallTool, assistant, user2}
	got, n = compactOversizedTailResults(msgs)
	if n != 0 {
		t.Errorf("expected 0 compacted for small result, got %d", n)
	}
	if ai.FlattenText(got[0].(ai.ToolResultMessage).Content) != "ok" {
		t.Error("small tool result should not be compacted")
	}

	// No user message — nothing compacted (safe fallback).
	msgs = []ai.Message{largeTool, assistant}
	got, n = compactOversizedTailResults(msgs)
	if n != 0 {
		t.Errorf("expected 0 compacted with no user message, got %d", n)
	}
	if ai.FlattenText(got[0].(ai.ToolResultMessage).Content) != largeText {
		t.Error("tool result should not be compacted when no user message present")
	}

	// Only one user message (first element) — nothing eligible before it.
	msgs = []ai.Message{user, largeTool}
	got, n = compactOversizedTailResults(msgs)
	if n != 0 {
		t.Errorf("expected 0 compacted when user is first element, got %d", n)
	}
	_ = got
}
