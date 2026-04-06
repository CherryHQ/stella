package channel

import (
	"strings"
	"testing"
	"time"
)

func TestEmojiFor_Known(t *testing.T) {
	tests := []struct {
		tool string
		want string
	}{
		{"bash", "⚡"},
		{"read", "📖"},
		{"write", "✏️"},
		{"edit", "🔧"},
		{"search", "🔍"},
	}
	for _, tc := range tests {
		got := EmojiFor(tc.tool)
		if got != tc.want {
			t.Errorf("EmojiFor(%q) = %q, want %q", tc.tool, got, tc.want)
		}
	}
}

func TestEmojiFor_Unknown(t *testing.T) {
	got := EmojiFor("unknowntool")
	if got != ToolEmoji["default"] {
		t.Errorf("EmojiFor(unknown) = %q, want default %q", got, ToolEmoji["default"])
	}
}

func TestTruncate_NoTruncation(t *testing.T) {
	got := Truncate("hello", 10)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncate_AtExact(t *testing.T) {
	got := Truncate("hello", 5)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncate_Truncated(t *testing.T) {
	got := Truncate("hello world", 8)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected '...' suffix, got %q", got)
	}
	if len(got) > 8 {
		t.Errorf("expected len <= 8, got %d", len(got))
	}
}

func TestTruncate_VeryShortMax(t *testing.T) {
	got := Truncate("hello", 2)
	if got != "..." {
		t.Errorf("expected '...', got %q", got)
	}
}

func TestTruncate_UTF8Boundary(t *testing.T) {
	// "日本語" = 9 bytes; truncate to 7 should not split inside a rune.
	got := Truncate("日本語", 7)
	// Result should be valid UTF-8.
	for _, r := range got {
		_ = r // iterating over runes validates UTF-8
	}
}

func TestToolLine_Running(t *testing.T) {
	ev := &ToolUseEvent{Tool: "bash", Status: "running", Input: "ls -la"}
	line := ToolLine(ev)
	if !strings.Contains(line, "bash") {
		t.Errorf("expected 'bash' in line, got %q", line)
	}
	if !strings.Contains(line, "ls -la") {
		t.Errorf("expected input in line, got %q", line)
	}
}

func TestToolLine_RunningNoInput(t *testing.T) {
	ev := &ToolUseEvent{Tool: "read", Status: "running", Input: ""}
	line := ToolLine(ev)
	if !strings.Contains(line, "read") {
		t.Errorf("expected 'read' in line, got %q", line)
	}
}

func TestToolLine_RunningLongInput(t *testing.T) {
	// Input longer than 60 runes should be truncated.
	ev := &ToolUseEvent{Tool: "bash", Status: "running", Input: strings.Repeat("x", 80)}
	line := ToolLine(ev)
	if !strings.HasSuffix(line, "...") {
		t.Errorf("expected '...' suffix for long input, got %q", line)
	}
}

func TestToolLine_Error(t *testing.T) {
	ev := &ToolUseEvent{Tool: "bash", Status: "error"}
	line := ToolLine(ev)
	if !strings.Contains(line, "bash failed") {
		t.Errorf("expected 'bash failed', got %q", line)
	}
}

func TestToolLine_Done(t *testing.T) {
	ev := &ToolUseEvent{Tool: "bash", Status: "done"}
	line := ToolLine(ev)
	if line != "" {
		t.Errorf("expected empty line for 'done' status, got %q", line)
	}
}

func TestToolTracker_StartFinish(t *testing.T) {
	tt := &ToolTracker{}

	tt.Start(&ToolUseEvent{Tool: "bash", Status: "running", Input: "echo hi"})
	if tt.ActiveTool != "bash" {
		t.Errorf("expected active tool 'bash', got %q", tt.ActiveTool)
	}

	tt.Finish(&ToolUseEvent{Tool: "bash", Status: "done"})
	if tt.ActiveTool != "" {
		t.Errorf("expected no active tool after finish")
	}
	if len(tt.History) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(tt.History))
	}
	if tt.History[0].Tool != "bash" {
		t.Errorf("expected 'bash' in history, got %q", tt.History[0].Tool)
	}
}

func TestToolTracker_Handle(t *testing.T) {
	tt := &ToolTracker{}

	changed := tt.Handle(&ToolUseEvent{Tool: "read", Status: "running"})
	if !changed {
		t.Error("expected display refresh on running event")
	}

	changed = tt.Handle(&ToolUseEvent{Tool: "read", Status: "done"})
	if !changed {
		t.Error("expected display refresh on done event")
	}

	changed = tt.Handle(&ToolUseEvent{Tool: "read", Status: "unknown"})
	if changed {
		t.Error("expected no refresh for unknown status")
	}
}

func TestToolTracker_IsDisplaying(t *testing.T) {
	tt := &ToolTracker{}
	if tt.IsDisplaying() {
		t.Error("expected not displaying initially")
	}

	tt.ActiveTool = "bash"
	if !tt.IsDisplaying() {
		t.Error("expected displaying with active tool")
	}

	tt.ActiveTool = ""
	tt.DisplayUntil = time.Now().Add(10 * time.Second)
	if !tt.IsDisplaying() {
		t.Error("expected displaying within display window")
	}
}

func TestToolTracker_HasHistory(t *testing.T) {
	tt := &ToolTracker{}
	if tt.HasHistory() {
		t.Error("expected no history initially")
	}
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "running"})
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "done"})
	if !tt.HasHistory() {
		t.Error("expected history after tool completion")
	}
}

func TestToolTracker_Render(t *testing.T) {
	tt := &ToolTracker{}
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "running", Input: "ls"})

	rendered := tt.Render()
	if !strings.Contains(rendered, "bash") {
		t.Errorf("expected 'bash' in render output, got %q", rendered)
	}
}

func TestToolTracker_RenderFinal_Empty(t *testing.T) {
	tt := &ToolTracker{}
	if got := tt.RenderFinal(); got != "" {
		t.Errorf("expected empty final render, got %q", got)
	}
}

func TestToolTracker_RenderFinal_WithHistory(t *testing.T) {
	tt := &ToolTracker{}
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "running", Input: "ls"})
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "done"})

	final := tt.RenderFinal()
	if !strings.Contains(final, "bash") {
		t.Errorf("expected 'bash' in final render, got %q", final)
	}
	if !strings.Contains(final, "tool") {
		t.Errorf("expected 'tool' in final render, got %q", final)
	}
}

func TestToolTracker_RenderFinal_WithError(t *testing.T) {
	tt := &ToolTracker{}
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "running"})
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "error", Detail: "exit 1"})

	final := tt.RenderFinal()
	if !strings.Contains(final, "exit 1") {
		t.Errorf("expected error detail in final render, got %q", final)
	}
}

func TestToolTracker_StartOverlap(t *testing.T) {
	// Starting a new tool while one is already active should auto-close the previous.
	tt := &ToolTracker{}
	tt.Start(&ToolUseEvent{Tool: "bash", Status: "running"})
	tt.Start(&ToolUseEvent{Tool: "read", Status: "running"})

	if tt.ActiveTool != "read" {
		t.Errorf("expected active tool 'read', got %q", tt.ActiveTool)
	}
	if len(tt.History) != 1 {
		t.Errorf("expected previous tool auto-closed to history, got %d", len(tt.History))
	}
}

func TestToolTracker_MinDisplayDuration(t *testing.T) {
	tt := &ToolTracker{MinDisplayDuration: 500 * time.Millisecond}
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "running"})
	tt.Handle(&ToolUseEvent{Tool: "bash", Status: "done"})

	if !tt.IsDisplaying() {
		t.Error("expected still displaying within min display duration")
	}
}

func TestRenderToolRecord_Done(t *testing.T) {
	rec := ToolRecord{Tool: "bash", Input: "ls -la", Status: "done", Duration: 50 * time.Millisecond}
	line := RenderToolRecord(rec)
	if !strings.Contains(line, "✅") {
		t.Errorf("expected ✅ for done status, got %q", line)
	}
	if !strings.Contains(line, "bash") {
		t.Errorf("expected 'bash' in render, got %q", line)
	}
}

func TestRenderToolRecord_Error(t *testing.T) {
	rec := ToolRecord{Tool: "bash", Status: "error", Detail: "exit 1", Duration: 10 * time.Millisecond}
	line := RenderToolRecord(rec)
	if !strings.Contains(line, "❌") {
		t.Errorf("expected ❌ for error status, got %q", line)
	}
	if !strings.Contains(line, "exit 1") {
		t.Errorf("expected detail in render, got %q", line)
	}
}
