package lcm

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

func TestToolResultImageRoundTrip(t *testing.T) {
	orig := ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "read",
		Content: []ai.ContentBlock{
			ai.TextContent{Text: "Read image file [image/png]"},
			ai.ImageContent{Data: "BASE64DATA", MimeType: "image/png"},
		},
	}

	rows := toolResultToRows(orig)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	restored := rowToToolResult(sqlc.CtxMessage{
		Role:      roleTool,
		EventType: eventTypeToolResult,
		Content:   rows[0].content,
	})

	if restored.ToolCallID != "tc1" || restored.ToolName != "read" {
		t.Fatalf("envelope metadata lost: %+v", restored)
	}
	if !ai.HasImage(restored.Content) {
		t.Fatal("image block lost on round-trip")
	}
	var img ai.ImageContent
	for _, b := range restored.Content {
		if ic, ok := b.(ai.ImageContent); ok {
			img = ic
		}
	}
	if img.Data != "BASE64DATA" || img.MimeType != "image/png" {
		t.Errorf("image not restored byte-identical: %+v", img)
	}
}

func TestToolResultTextOnlyRoundTrip(t *testing.T) {
	// Text-only results must not gain a Blocks field (keeps rows compact).
	rows := toolResultToRows(ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: "output"}},
	})
	var env toolResultEnvelope
	if err := json.Unmarshal([]byte(rows[0].content), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Blocks != nil {
		t.Errorf("text-only result must not store Blocks, got %v", env.Blocks)
	}
	restored := rowToToolResult(sqlc.CtxMessage{Role: roleTool, EventType: eventTypeToolResult, Content: rows[0].content})
	if got := ai.FlattenText(restored.Content); got != "output" {
		t.Errorf("text round-trip = %q, want output", got)
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

// --- Tool pair integrity tests ---

func makeCtxItem(ord int64, itemType, eventType string) sqlc.CtxItem {
	return sqlc.CtxItem{
		ConversationID: "conv1",
		Ordinal:        ord,
		ItemType:       itemType,
		EventType:      eventType,
		MessageID:      sql.NullString{String: "msg" + fmt.Sprintf("%d", ord), Valid: itemType == itemTypeMessage},
	}
}

func TestSplitFreshTail_ToolPairIntegrity(t *testing.T) {
	// Items: user(1), asst-text(2), asst-tool_call(3), tool_result(4), user(5), asst-text(6)
	// With freshTail=2, naive split would put items 5,6 in tail, leaving
	// tool_call(3) in older and tool_result(4) in tail — broken pair.
	items := []sqlc.CtxItem{
		makeCtxItem(1, itemTypeMessage, eventTypeText),
		makeCtxItem(2, itemTypeMessage, eventTypeText),
		makeCtxItem(3, itemTypeMessage, eventTypeToolCall),
		makeCtxItem(4, itemTypeMessage, eventTypeToolResult),
		makeCtxItem(5, itemTypeMessage, eventTypeText),
		makeCtxItem(6, itemTypeMessage, eventTypeText),
	}
	tail, older := splitFreshTail(items, 2)

	// tool_call(3) and tool_result(4) must be in the same partition.
	tailOrdinals := make(map[int64]bool)
	for _, item := range tail {
		tailOrdinals[item.Ordinal] = true
	}
	olderOrdinals := make(map[int64]bool)
	for _, item := range older {
		olderOrdinals[item.Ordinal] = true
	}
	// Both 3 and 4 should be in tail (pulled in to preserve the pair).
	if olderOrdinals[3] && tailOrdinals[4] {
		t.Error("splitFreshTail split tool_call(3) into older and tool_result(4) into tail")
	}
	if olderOrdinals[4] && tailOrdinals[3] {
		t.Error("splitFreshTail split tool_result(4) into older and tool_call(3) into tail")
	}
	// Verify they ended up together.
	if tailOrdinals[3] != tailOrdinals[4] {
		t.Errorf("tool_call and tool_result not in same partition: tail has 3=%v 4=%v", tailOrdinals[3], tailOrdinals[4])
	}
}

func TestSplitFreshTail_MultipleToolCalls(t *testing.T) {
	// Items: user(1), asst-tc1(2), asst-tc2(3), result1(4), result2(5), user(6)
	// freshTail=2 would naively land on item 5, splitting the tool pairs.
	items := []sqlc.CtxItem{
		makeCtxItem(1, itemTypeMessage, eventTypeText),
		makeCtxItem(2, itemTypeMessage, eventTypeToolCall),
		makeCtxItem(3, itemTypeMessage, eventTypeToolCall),
		makeCtxItem(4, itemTypeMessage, eventTypeToolResult),
		makeCtxItem(5, itemTypeMessage, eventTypeToolResult),
		makeCtxItem(6, itemTypeMessage, eventTypeText),
	}
	tail, older := splitFreshTail(items, 2)

	tailOrdinals := make(map[int64]bool)
	for _, item := range tail {
		tailOrdinals[item.Ordinal] = true
	}
	// All tool calls and results should be in the same partition.
	for _, ord := range []int64{2, 3, 4, 5} {
		if !tailOrdinals[ord] {
			t.Errorf("expected ordinal %d in tail, but it's in older", ord)
		}
	}
	_ = older
}

func TestTrimOrphanedToolPairs(t *testing.T) {
	tests := []struct {
		name     string
		items    []sqlc.CtxItem
		wantLen  int
		wantOrds []int64
	}{
		{
			name:     "empty",
			items:    nil,
			wantLen:  0,
			wantOrds: nil,
		},
		{
			name: "no orphans",
			items: []sqlc.CtxItem{
				makeCtxItem(1, itemTypeMessage, eventTypeText),
				makeCtxItem(2, itemTypeMessage, eventTypeToolCall),
				makeCtxItem(3, itemTypeMessage, eventTypeToolResult),
			},
			wantLen:  3,
			wantOrds: []int64{1, 2, 3},
		},
		{
			name: "leading orphan tool_result",
			items: []sqlc.CtxItem{
				makeCtxItem(1, itemTypeMessage, eventTypeToolResult),
				makeCtxItem(2, itemTypeMessage, eventTypeText),
				makeCtxItem(3, itemTypeMessage, eventTypeToolCall),
				makeCtxItem(4, itemTypeMessage, eventTypeToolResult),
			},
			wantLen:  3,
			wantOrds: []int64{2, 3, 4},
		},
		{
			name: "trailing orphan tool_call",
			items: []sqlc.CtxItem{
				makeCtxItem(1, itemTypeMessage, eventTypeText),
				makeCtxItem(2, itemTypeMessage, eventTypeToolCall),
				makeCtxItem(3, itemTypeMessage, eventTypeToolResult),
				makeCtxItem(4, itemTypeMessage, eventTypeToolCall),
			},
			wantLen:  3,
			wantOrds: []int64{1, 2, 3},
		},
		{
			name: "both ends orphaned",
			items: []sqlc.CtxItem{
				makeCtxItem(1, itemTypeMessage, eventTypeToolResult),
				makeCtxItem(2, itemTypeMessage, eventTypeToolResult),
				makeCtxItem(3, itemTypeMessage, eventTypeText),
				makeCtxItem(4, itemTypeMessage, eventTypeToolCall),
				makeCtxItem(5, itemTypeMessage, eventTypeToolCall),
			},
			wantLen:  1,
			wantOrds: []int64{3},
		},
		{
			name: "all orphans",
			items: []sqlc.CtxItem{
				makeCtxItem(1, itemTypeMessage, eventTypeToolResult),
				makeCtxItem(2, itemTypeMessage, eventTypeToolCall),
			},
			wantLen:  0,
			wantOrds: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trimOrphanedToolPairs(tc.items)
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
			for i, want := range tc.wantOrds {
				if got[i].Ordinal != want {
					t.Errorf("got[%d].Ordinal = %d, want %d", i, got[i].Ordinal, want)
				}
			}
		})
	}
}

func TestFindMessageRuns_ToolPairBoundary(t *testing.T) {
	// Items: [msg, msg, ..., tool_call, summary, tool_result, msg, msg, ...]
	// The summary breaks the run. The first run should not end with orphan tool_call,
	// and the second run should not start with orphan tool_result.
	var items []sqlc.CtxItem
	for i := int64(1); i <= 10; i++ {
		items = append(items, makeCtxItem(i, itemTypeMessage, eventTypeText))
	}
	items[8].EventType = eventTypeToolCall // item at ordinal 9
	// Insert a summary between ordinals 9 and 10.
	items = append(items[:9], append([]sqlc.CtxItem{
		{ConversationID: "conv1", Ordinal: 10, ItemType: itemTypeSummary, SummaryID: sql.NullString{String: "sum1", Valid: true}},
	}, sqlc.CtxItem{ConversationID: "conv1", Ordinal: 11, ItemType: itemTypeMessage, EventType: eventTypeToolResult, MessageID: sql.NullString{String: "msg11", Valid: true}})...)
	for i := int64(12); i <= 20; i++ {
		items = append(items, makeCtxItem(i, itemTypeMessage, eventTypeText))
	}

	runs := findMessageRuns(items, 3)
	for _, run := range runs {
		first := run.items[0]
		last := run.items[len(run.items)-1]
		if first.EventType == eventTypeToolResult {
			t.Errorf("run starting at ordinal %d begins with orphan tool_result", run.startOrd)
		}
		if last.EventType == eventTypeToolCall {
			t.Errorf("run ending at ordinal %d ends with orphan tool_call", run.endOrd)
		}
	}
}

func TestStripTrailingOrphanResults(t *testing.T) {
	tc1 := ai.ToolCall{ID: "call1", Name: "bash", Arguments: nil}
	tc2 := ai.ToolCall{ID: "call2", Name: "read", Arguments: nil}
	asst := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "thinking"}, tc1}}
	tr1 := ai.ToolResultMessage{ToolCallID: "call1", ToolName: "bash", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}}
	tr2 := ai.ToolResultMessage{ToolCallID: "call2", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "data"}}}

	// tr2 is orphan (call2 not in any assistant message).
	msgs := []ai.Message{asst, tr1, tr2}
	got := stripTrailingOrphanResults(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages after strip, got %d", len(got))
	}

	// Both have matching calls — nothing stripped.
	asst2 := ai.AssistantMessage{Content: []ai.ContentBlock{tc1, tc2}}
	msgs = []ai.Message{asst2, tr1, tr2}
	got = stripTrailingOrphanResults(msgs)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages (no orphans), got %d", len(got))
	}

	// Empty input.
	got = stripTrailingOrphanResults(nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 for nil input, got %d", len(got))
	}
}

func TestSanitizeToolPairs_OrphanResult(t *testing.T) {
	// A tool_result with no matching tool_call should become a UserMessage.
	msgs := []ai.Message{
		ai.UserMessage{Content: "hello"},
		ai.ToolResultMessage{
			ToolCallID: "orphan_call",
			ToolName:   "bash",
			Content:    []ai.ContentBlock{ai.TextContent{Text: "some output"}},
		},
	}
	got := sanitizeToolPairs(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	um, ok := got[1].(ai.UserMessage)
	if !ok {
		t.Fatalf("expected UserMessage, got %T", got[1])
	}
	content, _ := um.Content.(string)
	if !strings.Contains(content, "bash") || !strings.Contains(content, "some output") {
		t.Errorf("converted UserMessage should mention tool name and content, got %q", content)
	}
}

func TestSanitizeToolPairs_OrphanCallInNonFinal(t *testing.T) {
	tc := ai.ToolCall{ID: "call1", Name: "bash"}
	asst1 := ai.AssistantMessage{Content: []ai.ContentBlock{tc}}
	asst2 := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "final"}}}

	// asst1 has a tool_call with no result, and it's not the final assistant message.
	msgs := []ai.Message{asst1, ai.UserMessage{Content: "next"}, asst2}
	got := sanitizeToolPairs(msgs)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	// The orphan call in asst1 should be stripped, replaced with placeholder.
	cleaned, ok := got[0].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("expected AssistantMessage at 0, got %T", got[0])
	}
	if len(cleaned.Content) != 1 {
		t.Fatalf("expected 1 block (placeholder), got %d", len(cleaned.Content))
	}
	text, ok := cleaned.Content[0].(ai.TextContent)
	if !ok || !strings.Contains(text.Text, "compacted") {
		t.Errorf("expected placeholder text, got %v", cleaned.Content[0])
	}
}

func TestSanitizeToolPairs_InProgressCall(t *testing.T) {
	// The final assistant message may have an in-progress tool_call — preserve it.
	tc := ai.ToolCall{ID: "call1", Name: "bash"}
	asst := ai.AssistantMessage{Content: []ai.ContentBlock{tc}}
	msgs := []ai.Message{ai.UserMessage{Content: "do something"}, asst}
	got := sanitizeToolPairs(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	finalAsst, ok := got[1].(ai.AssistantMessage)
	if !ok {
		t.Fatalf("expected AssistantMessage at 1, got %T", got[1])
	}
	if len(finalAsst.Content) != 1 {
		t.Fatalf("expected 1 block preserved, got %d", len(finalAsst.Content))
	}
	if _, ok := finalAsst.Content[0].(ai.ToolCall); !ok {
		t.Error("in-progress tool_call on final assistant message should be preserved")
	}
}

func TestSanitizeToolPairs_NoOrphans(t *testing.T) {
	tc := ai.ToolCall{ID: "call1", Name: "bash"}
	asst := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "let me check"}, tc}}
	tr := ai.ToolResultMessage{ToolCallID: "call1", ToolName: "bash", Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}}
	asst2 := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}}

	msgs := []ai.Message{ai.UserMessage{Content: "hi"}, asst, tr, asst2}
	got := sanitizeToolPairs(msgs)
	if len(got) != 4 {
		t.Fatalf("expected 4 messages unchanged, got %d", len(got))
	}
	// Verify types preserved.
	if _, ok := got[1].(ai.AssistantMessage); !ok {
		t.Error("message 1 should remain AssistantMessage")
	}
	if _, ok := got[2].(ai.ToolResultMessage); !ok {
		t.Error("message 2 should remain ToolResultMessage")
	}
}

func TestSanitizeToolPairs_EmptyOrphanResultDropped(t *testing.T) {
	msgs := []ai.Message{
		ai.ToolResultMessage{
			ToolCallID: "orphan",
			ToolName:   "bash",
			Content:    []ai.ContentBlock{ai.TextContent{Text: ""}},
		},
	}
	got := sanitizeToolPairs(msgs)
	if len(got) != 0 {
		t.Fatalf("expected empty orphan result to be dropped, got %d messages", len(got))
	}
}
