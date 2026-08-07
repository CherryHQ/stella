package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"
)

type telegramProvisioningHandler struct {
	fakeChannelHandler
	calls int
	err   error
}

func (h *telegramProvisioningHandler) EnsurePlatformGroupMember(ctx context.Context, _, _, _ string) error {
	h.calls++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return h.err
}

func TestEnsureGroupMemberFailsClosedAndCachesOnlySuccess(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	if (&Bot{handler: fakeChannelHandler{}, ctx: lifecycle}).ensureGroupMember("-100") {
		t.Fatal("admitted without capability")
	}
	h := &telegramProvisioningHandler{err: errors.New("unavailable")}
	b := &Bot{handler: h, ctx: lifecycle}
	if b.ensureGroupMember("-100") {
		t.Fatal("admitted after failure")
	}
	if b.ensureGroupMember("-100") {
		t.Fatal("admitted cached failure")
	}
	if h.calls != 1 {
		t.Fatalf("calls = %d, want 1 during failure TTL", h.calls)
	}
	h.err = nil
	b.provisionMu.Lock()
	b.provisionFailures["-100"] = time.Now().Add(-time.Second)
	b.provisionMu.Unlock()
	if !b.ensureGroupMember("-100") {
		t.Fatal("rejected success")
	}
	if !b.ensureGroupMember("-100") {
		t.Fatal("rejected cached success")
	}
	if h.calls != 2 {
		t.Fatalf("calls = %d, want 2", h.calls)
	}
	cancel()
	other := &telegramProvisioningHandler{}
	if (&Bot{handler: other, ctx: lifecycle}).ensureGroupMember("-200") {
		t.Fatal("admitted after lifecycle cancellation")
	}
}

func TestSplitMessageShort(t *testing.T) {
	chunks := channel.SplitMessage("hello", telegramMaxMessageLen)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("chunks = %v, want [hello]", chunks)
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	msg := strings.Repeat("a", telegramMaxMessageLen)
	chunks := channel.SplitMessage(msg, telegramMaxMessageLen)
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestSplitMessageLong(t *testing.T) {
	msg := strings.Repeat("a", telegramMaxMessageLen+100)
	chunks := channel.SplitMessage(msg, telegramMaxMessageLen)
	if len(chunks) != 2 {
		t.Errorf("len(chunks) = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != telegramMaxMessageLen {
		t.Errorf("chunk[0] len = %d, want %d", len(chunks[0]), telegramMaxMessageLen)
	}
	if len(chunks[1]) != 100 {
		t.Errorf("chunk[1] len = %d, want 100", len(chunks[1]))
	}
}

func TestSplitMessageAtNewline(t *testing.T) {
	part1 := strings.Repeat("a", 3000)
	part2 := strings.Repeat("b", 2000)
	msg := part1 + "\n" + part2

	chunks := channel.SplitMessage(msg, telegramMaxMessageLen)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0] != part1+"\n" {
		t.Errorf("chunk[0] = %q..., want split at newline", chunks[0][:20])
	}
	if chunks[1] != part2 {
		t.Errorf("chunk[1] len = %d, want %d", len(chunks[1]), len(part2))
	}
}

func TestSplitMessageEmpty(t *testing.T) {
	chunks := channel.SplitMessage("", telegramMaxMessageLen)
	if len(chunks) != 1 || chunks[0] != "" {
		t.Errorf("chunks = %v, want [\"\"]", chunks)
	}
}

func TestSplitMessageMultipleChunks(t *testing.T) {
	msg := strings.Repeat("x", telegramMaxMessageLen*3+500)
	chunks := channel.SplitMessage(msg, telegramMaxMessageLen)
	if len(chunks) != 4 {
		t.Errorf("len(chunks) = %d, want 4", len(chunks))
	}
	var rebuilt strings.Builder
	for _, c := range chunks {
		rebuilt.WriteString(c)
	}
	if rebuilt.String() != msg {
		t.Error("chunks do not reconstruct to original message")
	}
}

func TestRenderMarkdown(t *testing.T) {
	md := tgmd.TGMD()

	tests := []struct {
		name  string
		input string
	}{
		{"bold", "**bold text**"},
		{"code block", "```go\nfmt.Println()\n```"},
		{"plain text", "just plain text"},
		{"empty", " "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := renderMarkdown(md, tt.input)
			if result == "" {
				t.Error("renderMarkdown returned empty string")
			}
		})
	}
}

func TestRenderMarkdownFallback(t *testing.T) {
	md := tgmd.TGMD()
	// Plain text should still return something non-empty.
	result := renderMarkdown(md, "hello world")
	if result == "" {
		t.Error("expected non-empty result for plain text")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	md := tgmd.TGMD()
	// If converter produces empty output, fall back to original text.
	// Passing empty string should return the original text.
	result := renderMarkdown(md, "")
	// Either empty or the original; just verify no panic and non-nil.
	_ = result
}

func TestBotCommands(t *testing.T) {
	commands := botCommands()
	if len(commands) != 7 {
		t.Fatalf("len(commands) = %d, want 7", len(commands))
	}

	want := []string{"start", "new", "compact", "abort", "model", "agent", "whoami"}
	for i, cmd := range commands {
		if cmd.Text != want[i] {
			t.Errorf("commands[%d].Text = %q, want %q", i, cmd.Text, want[i])
		}
	}
}

func TestToolTracker(t *testing.T) {
	var tracker channel.ToolTracker

	// Start a tool.
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls -la"})
	if tracker.ActiveTool != "bash" {
		t.Errorf("activeTool = %q, want %q", tracker.ActiveTool, "bash")
	}
	if !tracker.IsDisplaying() {
		t.Error("expected isDisplaying=true while tool is active")
	}

	rendered := tracker.Render()
	if !strings.Contains(rendered, "⏳") {
		t.Errorf("render() = %q, want spinner emoji", rendered)
	}
	if !strings.Contains(rendered, "bash") {
		t.Errorf("render() = %q, want tool name", rendered)
	}
	if !strings.Contains(rendered, "ls -la") {
		t.Errorf("render() = %q, want tool input", rendered)
	}

	// Finish the tool.
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "done", Input: "ls -la"})
	if tracker.ActiveTool != "" {
		t.Errorf("activeTool = %q, want empty after done", tracker.ActiveTool)
	}
	if len(tracker.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(tracker.History))
	}
	if tracker.History[0].Tool != "bash" {
		t.Errorf("history[0].Tool = %q, want %q", tracker.History[0].Tool, "bash")
	}

	rendered = tracker.Render()
	if !strings.Contains(rendered, "✅") {
		t.Errorf("render() = %q, want checkmark for done tool", rendered)
	}
	if !strings.Contains(rendered, "bash") {
		t.Errorf("render() = %q, want tool name in history", rendered)
	}
}

func TestToolTrackerError(t *testing.T) {
	var tracker channel.ToolTracker
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "exit 1"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "error", Detail: "command failed"})

	if len(tracker.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(tracker.History))
	}
	rec := tracker.History[0]
	if rec.Status != "error" {
		t.Errorf("status = %q, want %q", rec.Status, "error")
	}

	rendered := tracker.Render()
	if !strings.Contains(rendered, "❌") {
		t.Errorf("render() = %q, want error emoji", rendered)
	}
	if !strings.Contains(rendered, "command failed") {
		t.Errorf("render() = %q, want error detail", rendered)
	}
}

func TestToolTrackerMultipleTools(t *testing.T) {
	var tracker channel.ToolTracker
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "main.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done", Input: "main.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "edit", Status: "running", Input: "main.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "edit", Status: "done", Input: "main.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "go test"})

	if len(tracker.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(tracker.History))
	}
	if tracker.ActiveTool != "bash" {
		t.Errorf("activeTool = %q, want %q", tracker.ActiveTool, "bash")
	}

	rendered := tracker.Render()
	// Should contain two completed tools and one active.
	if strings.Count(rendered, "✅") != 2 {
		t.Errorf("render() has %d checkmarks, want 2", strings.Count(rendered, "✅"))
	}
	if !strings.Contains(rendered, "⏳") {
		t.Error("render() missing spinner for active tool")
	}
}

func TestToolTrackerRenderFinal(t *testing.T) {
	var tracker channel.ToolTracker

	// No history → empty string.
	if got := tracker.RenderFinal(); got != "" {
		t.Errorf("renderFinal() with no history = %q, want empty", got)
	}

	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "main.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done", Detail: "42 lines"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "go test"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "error", Detail: "exit 1"})

	got := tracker.RenderFinal()
	if !strings.Contains(got, "——————————————————") {
		t.Error("renderFinal() missing separator line")
	}
	// Compact one-liner with tool counts.
	if !strings.Contains(got, "📎 2 tools") {
		t.Error("renderFinal() missing compact tool count")
	}
	if !strings.Contains(got, "read") || !strings.Contains(got, "bash") {
		t.Error("renderFinal() missing tool names in summary")
	}
	// Error tool should be shown in detail.
	if !strings.Contains(got, "❌") {
		t.Error("renderFinal() missing error line")
	}
	if !strings.Contains(got, "exit 1") {
		t.Error("renderFinal() missing error detail")
	}
	// Successful tools should NOT have individual lines.
	if strings.Contains(got, "✅") {
		t.Error("renderFinal() should not have individual success lines")
	}
}

func TestToolTrackerRenderFinalAllSuccess(t *testing.T) {
	var tracker channel.ToolTracker
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "a.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "b.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "go test"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "done"})

	got := tracker.RenderFinal()
	if !strings.Contains(got, "📎 3 tools") {
		t.Errorf("renderFinal() = %q, want '3 tools'", got)
	}
	if !strings.Contains(got, "2× 📖read") {
		t.Errorf("renderFinal() = %q, want '2× 📖read'", got)
	}
	if !strings.Contains(got, "⚡bash") {
		t.Errorf("renderFinal() = %q, want '⚡bash'", got)
	}
	// No error lines.
	if strings.Contains(got, "❌") {
		t.Error("renderFinal() should not have error lines for all-success")
	}
}

func TestToolTrackerRenderFinalSingleTool(t *testing.T) {
	var tracker channel.ToolTracker
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "x.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done"})

	got := tracker.RenderFinal()
	if !strings.Contains(got, "📎 1 tool (") {
		t.Errorf("renderFinal() = %q, want singular 'tool'", got)
	}
}

func TestToolTrackerHasHistory(t *testing.T) {
	var tracker channel.ToolTracker
	if tracker.HasHistory() {
		t.Error("hasHistory() should be false with no tools")
	}
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "x"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done", Input: "x"})
	if !tracker.HasHistory() {
		t.Error("hasHistory() should be true after tool finished")
	}
}

func TestToolTrackerMinDisplayDuration(t *testing.T) {
	tracker := channel.ToolTracker{MinDisplayDuration: 2 * time.Second}
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "file.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done", Input: "file.go"})

	// Right after finishing, should still be displaying (minimum duration).
	if !tracker.IsDisplaying() {
		t.Error("expected isDisplaying=true right after tool finished")
	}
}

func TestToolTrackerStartOverwritesActive(t *testing.T) {
	var tracker channel.ToolTracker
	// Start one tool, then start another without finishing the first.
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "a.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls"})

	// The first tool should be auto-finished in history.
	if len(tracker.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(tracker.History))
	}
	if tracker.History[0].Tool != "read" {
		t.Errorf("history[0].Tool = %q, want %q", tracker.History[0].Tool, "read")
	}
	if tracker.ActiveTool != "bash" {
		t.Errorf("activeTool = %q, want %q", tracker.ActiveTool, "bash")
	}
}

func TestRenderToolRecord(t *testing.T) {
	tests := []struct {
		name       string
		rec        channel.ToolRecord
		wantParts  []string
		wantAbsent []string
	}{
		{
			name:      "done with input and detail",
			rec:       channel.ToolRecord{Tool: "bash", Input: "ls -la", Status: "done", Detail: "3 files", Duration: 500 * time.Millisecond},
			wantParts: []string{"✅", "⚡", "bash", "ls -la", "→ 3 files", "500ms"},
		},
		{
			name:      "done seconds",
			rec:       channel.ToolRecord{Tool: "read", Input: "main.go", Status: "done", Detail: "42 lines", Duration: 2500 * time.Millisecond},
			wantParts: []string{"✅", "📖", "read", "main.go", "→ 42 lines", "2.5s"},
		},
		{
			name:      "error with detail",
			rec:       channel.ToolRecord{Tool: "bash", Input: "rm -rf /", Status: "error", Detail: "permission denied", Duration: time.Second},
			wantParts: []string{"❌", "bash", "rm -rf /", "→ permission denied"},
		},
		{
			name:       "done no input no detail",
			rec:        channel.ToolRecord{Tool: "search", Status: "done", Duration: 100 * time.Millisecond},
			wantParts:  []string{"✅", "🔍", "search", "100ms"},
			wantAbsent: []string{": ", "→"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := channel.RenderToolRecord(tt.rec)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("channel.RenderToolRecord() = %q, want to contain %q", got, part)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("channel.RenderToolRecord() = %q, should not contain %q", got, absent)
				}
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello..."},
		{"abc", 3, "abc"},
		{"abcdef", 5, "ab..."},
	}

	for _, tt := range tests {
		got := channel.Truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("channel.Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{0, "0ms"},
		{time.Second, "1.0s"},
		{2500 * time.Millisecond, "2.5s"},
		{10 * time.Second, "10.0s"},
	}

	for _, tt := range tests {
		got := channel.FormatDuration(tt.d)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestEmojiFor(t *testing.T) {
	tests := []struct {
		tool string
		want string
	}{
		{"bash", "⚡"},
		{"read", "📖"},
		{"write", "✏️"},
		{"edit", "🔧"},
		{"search", "🔍"},
		{"unknown_tool", "🔧"},
	}

	for _, tt := range tests {
		got := channel.EmojiFor(tt.tool)
		if got != tt.want {
			t.Errorf("channel.EmojiFor(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestToolEmojiDefaults(t *testing.T) {
	// Verify all documented tools have emoji entries.
	for _, tool := range []string{"bash", "read", "write", "edit", "search"} {
		if _, ok := channel.ToolEmoji[tool]; !ok {
			t.Errorf("missing emoji for tool %q", tool)
		}
	}
	if _, ok := channel.ToolEmoji["default"]; !ok {
		t.Error("missing default emoji")
	}
}

func TestBuildStreamDisplay(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		toolSection string
		hasTools    bool
		wantSuffix  string
	}{
		{
			name:       "text only",
			text:       "hello",
			wantSuffix: typingCursor,
		},
		{
			name:        "with tool section",
			text:        "hello",
			toolSection: "✅ ⚡ bash: ls (100ms)\n",
			hasTools:    true,
			wantSuffix:  "\n\n✅ ⚡ bash: ls (100ms)" + typingCursor,
		},
		{
			name:       "empty text no tools",
			text:       "",
			wantSuffix: typingCursor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildStreamDisplay(tt.text, tt.toolSection, tt.hasTools)
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("buildStreamDisplay() = %q, want suffix %q", got, tt.wantSuffix)
			}
			if !strings.HasPrefix(got, tt.text) {
				t.Errorf("buildStreamDisplay() = %q, want prefix %q", got, tt.text)
			}
		})
	}
}

func TestBuildStreamDisplayTruncation(t *testing.T) {
	longText := strings.Repeat("a", telegramMaxMessageLen+500)
	got := buildStreamDisplay(longText, "", false)
	if len(got) > telegramMaxMessageLen {
		t.Errorf("buildStreamDisplay() len = %d, want <= %d", len(got), telegramMaxMessageLen)
	}
	if !strings.HasSuffix(got, "..."+typingCursor) {
		t.Error("truncated display should end with ...cursor")
	}
}

func TestBuildStreamDisplayUTF8Safe(t *testing.T) {
	// Build a string that would need truncation, with multi-byte runes near the cut point.
	prefix := strings.Repeat("a", telegramMaxMessageLen-20)
	multibyte := strings.Repeat("世", 20) // 60 bytes, will push past limit
	text := prefix + multibyte

	got := buildStreamDisplay(text, "", false)
	if strings.ToValidUTF8(got, "?") != got {
		t.Error("buildStreamDisplay() produced invalid UTF-8")
	}
}

func TestBuildStreamDisplay_LongSuffix(t *testing.T) {
	// Craft a tool section so long it exceeds telegramMaxMessageLen.
	// In that case suffix is reset to just typingCursor.
	longTool := strings.Repeat("x", telegramMaxMessageLen+100)
	got := buildStreamDisplay("text", longTool, true)
	// The result should still be valid and end with typingCursor.
	if !strings.HasSuffix(got, typingCursor) {
		t.Errorf("expected typingCursor suffix for long tool section, got %q", got[:min(len(got), 50)])
	}
}

// --- atoiOr ---

func TestAtoiOrValid(t *testing.T) {
	if got := atoiOr("42", 0); got != 42 {
		t.Errorf("atoiOr(42) = %d, want 42", got)
	}
}

func TestAtoiOrInvalid(t *testing.T) {
	if got := atoiOr("abc", 99); got != 99 {
		t.Errorf("atoiOr(abc) = %d, want 99", got)
	}
}

func TestAtoiOrEmpty(t *testing.T) {
	if got := atoiOr("", 5); got != 5 {
		t.Errorf("atoiOr('') = %d, want 5", got)
	}
}

func TestAtoiOrNegative(t *testing.T) {
	if got := atoiOr("-3", 0); got != -3 {
		t.Errorf("atoiOr(-3) = %d, want -3", got)
	}
}

func TestAtoiOrZero(t *testing.T) {
	if got := atoiOr("0", 10); got != 0 {
		t.Errorf("atoiOr(0) = %d, want 0", got)
	}
}

// --- truncate edge cases ---

func TestTruncateEmpty(t *testing.T) {
	if got := channel.Truncate("", 10); got != "" {
		t.Errorf("channel.Truncate('', 10) = %q, want empty", got)
	}
}

func TestTruncateExact(t *testing.T) {
	if got := channel.Truncate("abc", 3); got != "abc" {
		t.Errorf("channel.Truncate(abc, 3) = %q, want abc", got)
	}
}

// --- chatRef ---

func TestChatRefFormatsCorrectly(t *testing.T) {
	ref := chatRef("@mychannel")
	if ref.Recipient() != "@mychannel" {
		t.Errorf("Recipient() = %q, want @mychannel", ref.Recipient())
	}
}

// --- welcomeMessage ---

func TestWelcomeMessageContainsCommands(t *testing.T) {
	msg := channel.WelcomeMessage
	for _, cmd := range []string{"/new", "/compact", "/abort", "/model", "/whoami"} {
		if !strings.Contains(msg, cmd) {
			t.Errorf("WelcomeMessage missing %q", cmd)
		}
	}
}
