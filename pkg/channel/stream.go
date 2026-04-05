package channel

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ToolEmoji maps known tool names to display emoji.
var ToolEmoji = map[string]string{
	"bash":    "⚡",
	"read":    "📖",
	"write":   "✏️",
	"edit":    "🔧",
	"search":  "🔍",
	"default": "🔧",
}

// EmojiFor returns the emoji for a tool name, falling back to the default.
func EmojiFor(tool string) string {
	if e, ok := ToolEmoji[tool]; ok {
		return e
	}
	return ToolEmoji["default"]
}

// Truncate shortens s to maxLen bytes, appending "..." if truncated.
// Cuts at a valid UTF-8 boundary to avoid producing invalid strings.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."
	}
	cutAt := maxLen - 3
	for cutAt > 0 && !utf8.RuneStart(s[cutAt]) {
		cutAt--
	}
	return s[:cutAt] + "..."
}

// ToolLine returns a short status line for a tool-use event.
// Used by channels with simple tool display (no full tracker).
func ToolLine(t *ToolUseEvent) string {
	emoji := EmojiFor(t.Tool)
	switch t.Status {
	case "running":
		input := t.Input
		if utf8.RuneCountInString(input) > 60 {
			r := []rune(input)
			input = string(r[:57]) + "..."
		}
		if input != "" {
			return fmt.Sprintf("%s %s: %s", emoji, t.Tool, input)
		}
		return fmt.Sprintf("%s %s", emoji, t.Tool)
	case "error":
		return fmt.Sprintf("❌ %s failed", t.Tool)
	default:
		return ""
	}
}

// ToolRecord holds a completed tool invocation for summary display.
type ToolRecord struct {
	Tool     string
	Input    string
	Status   string // "done" or "error"
	Detail   string
	Duration time.Duration
}

// ToolTracker tracks active and completed tool invocations during streaming.
type ToolTracker struct {
	History      []ToolRecord
	ActiveTool   string
	ActiveInput  string
	ActiveStart  time.Time
	DisplayUntil time.Time // minimum time to keep showing the last-finished tool

	// MinDisplayDuration controls how long a finished tool stays visible.
	// Zero means no minimum.
	MinDisplayDuration time.Duration
}

// Start registers a new tool as running.
func (tt *ToolTracker) Start(t *ToolUseEvent) {
	// If a tool was already active, finish it as "done" (missed the finish event).
	if tt.ActiveTool != "" {
		tt.History = append(tt.History, ToolRecord{
			Tool:     tt.ActiveTool,
			Input:    tt.ActiveInput,
			Status:   "done",
			Duration: time.Since(tt.ActiveStart),
		})
	}
	tt.ActiveTool = t.Tool
	tt.ActiveInput = t.Input
	tt.ActiveStart = time.Now()
	tt.DisplayUntil = time.Time{}
}

// Finish records the active tool as completed.
func (tt *ToolTracker) Finish(t *ToolUseEvent) {
	dur := time.Since(tt.ActiveStart)
	input := tt.ActiveInput
	if t.Input != "" {
		input = t.Input
	}
	tt.History = append(tt.History, ToolRecord{
		Tool:     t.Tool,
		Input:    input,
		Status:   t.Status,
		Detail:   t.Detail,
		Duration: dur,
	})
	if tt.MinDisplayDuration > 0 {
		tt.DisplayUntil = time.Now().Add(tt.MinDisplayDuration)
	}
	tt.ActiveTool = ""
	tt.ActiveInput = ""
	tt.ActiveStart = time.Time{}
}

// Handle processes a tool event, returning true if a display refresh is needed.
func (tt *ToolTracker) Handle(t *ToolUseEvent) bool {
	switch t.Status {
	case "running":
		tt.Start(t)
		return true
	case "done", "error":
		tt.Finish(t)
		return true
	}
	return false
}

// IsDisplaying returns true if there is an active tool or the minimum display
// duration for the last finished tool has not yet elapsed.
func (tt *ToolTracker) IsDisplaying() bool {
	if tt.ActiveTool != "" {
		return true
	}
	return time.Now().Before(tt.DisplayUntil)
}

// HasHistory returns true if any tools were tracked.
func (tt *ToolTracker) HasHistory() bool {
	return len(tt.History) > 0
}

// Render builds the tool section for real-time streaming display,
// showing completed tools and the active tool with elapsed time.
func (tt *ToolTracker) Render() string {
	var sb strings.Builder

	for _, rec := range tt.History {
		sb.WriteString(RenderToolRecord(rec))
		sb.WriteByte('\n')
	}

	if tt.ActiveTool != "" {
		emoji := EmojiFor(tt.ActiveTool)
		elapsed := time.Since(tt.ActiveStart).Truncate(100 * time.Millisecond)
		line := fmt.Sprintf("⏳ %s %s", emoji, tt.ActiveTool)
		if tt.ActiveInput != "" {
			input := Truncate(tt.ActiveInput, 60)
			line += ": " + input
		}
		line += fmt.Sprintf(" (%s)", FormatDuration(elapsed))
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	return sb.String()
}

// RenderFinal builds a compact tool summary for the final message.
// Shows a one-liner with tool counts and total time, plus individual
// lines for any failed tool calls.
func (tt *ToolTracker) RenderFinal() string {
	if len(tt.History) == 0 {
		return ""
	}

	type toolCount struct {
		name  string
		count int
	}
	seen := map[string]int{}
	var counts []toolCount
	var totalDur time.Duration
	var errors []ToolRecord

	for _, rec := range tt.History {
		totalDur += rec.Duration
		if rec.Status == "error" {
			errors = append(errors, rec)
		}
		if idx, ok := seen[rec.Tool]; ok {
			counts[idx].count++
		} else {
			seen[rec.Tool] = len(counts)
			counts = append(counts, toolCount{name: rec.Tool, count: 1})
		}
	}

	var sb strings.Builder
	sb.WriteString("\n\n——————————————————\n")

	total := len(tt.History)
	fmt.Fprintf(&sb, "📎 %d tool", total)
	if total != 1 {
		sb.WriteByte('s')
	}
	sb.WriteString(" (")
	for i, tc := range counts {
		if i > 0 {
			sb.WriteString(", ")
		}
		emoji := EmojiFor(tc.name)
		if tc.count > 1 {
			fmt.Fprintf(&sb, "%d× %s%s", tc.count, emoji, tc.name)
		} else {
			sb.WriteString(emoji + tc.name)
		}
	}
	sb.WriteString(") · ")
	sb.WriteString(FormatDuration(totalDur))

	for _, rec := range errors {
		sb.WriteByte('\n')
		sb.WriteString(RenderToolRecord(rec))
	}

	return sb.String()
}

// RenderToolRecord formats a single completed tool record.
func RenderToolRecord(rec ToolRecord) string {
	statusEmoji := "✅"
	if rec.Status == "error" {
		statusEmoji = "❌"
	}
	emoji := EmojiFor(rec.Tool)
	line := fmt.Sprintf("%s %s %s", statusEmoji, emoji, rec.Tool)
	if rec.Input != "" {
		input := Truncate(rec.Input, 60)
		line += ": " + input
	}
	if rec.Detail != "" {
		detail := Truncate(rec.Detail, 80)
		line += " → " + detail
	}
	line += fmt.Sprintf(" (%s)", FormatDuration(rec.Duration))
	return line
}
