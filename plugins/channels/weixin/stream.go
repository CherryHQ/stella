package weixin

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vaayne/anna/pkg/channel"
)

const (
	// weixinMaxMessageLen is the maximum text message length for WeChat iLink.
	weixinMaxMessageLen = 2000

	// typingInterval is how often we re-send the typing indicator.
	// WeChat typing status expires after a few seconds.
	typingInterval = 5 * time.Second
)

// toolEmoji maps known tool names to display emoji.
var toolEmoji = map[string]string{
	"bash":    "⚡",
	"read":    "📖",
	"write":   "✏️",
	"edit":    "🔧",
	"search":  "🔍",
	"default": "🔧",
}

// toolRecord holds a completed tool invocation for the summary section.
type toolRecord struct {
	Tool     string
	Input    string
	Status   string // "done" or "error"
	Detail   string
	Duration time.Duration
}

// toolTracker tracks active and completed tool invocations during streaming.
type toolTracker struct {
	history      []toolRecord
	activeTool   string
	activeInput  string
	activeStart  time.Time
	displayUntil time.Time
}

// start registers a new tool as running.
func (tt *toolTracker) start(t *channel.ToolUseEvent) {
	if tt.activeTool != "" {
		tt.history = append(tt.history, toolRecord{
			Tool:     tt.activeTool,
			Input:    tt.activeInput,
			Status:   "done",
			Duration: time.Since(tt.activeStart),
		})
	}
	tt.activeTool = t.Tool
	tt.activeInput = t.Input
	tt.activeStart = time.Now()
	tt.displayUntil = time.Time{}
}

// finish records the active tool as completed.
func (tt *toolTracker) finish(t *channel.ToolUseEvent) {
	dur := time.Since(tt.activeStart)
	input := tt.activeInput
	if t.Input != "" {
		input = t.Input
	}
	tt.history = append(tt.history, toolRecord{
		Tool:     t.Tool,
		Input:    input,
		Status:   t.Status,
		Detail:   t.Detail,
		Duration: dur,
	})
	tt.displayUntil = time.Now().Add(2 * time.Second)
	tt.activeTool = ""
	tt.activeInput = ""
	tt.activeStart = time.Time{}
}

// handle processes a tool event, returning true if a display refresh is needed.
func (tt *toolTracker) handle(t *channel.ToolUseEvent) bool {
	switch t.Status {
	case "running":
		tt.start(t)
		return true
	case "done", "error":
		tt.finish(t)
		return true
	}
	return false
}

// hasHistory returns true if any tools were tracked.
func (tt *toolTracker) hasHistory() bool {
	return len(tt.history) > 0
}

// renderFinal builds a compact tool summary for the final message.
func (tt *toolTracker) renderFinal() string {
	if len(tt.history) == 0 {
		return ""
	}

	type toolCount struct {
		name  string
		count int
	}
	seen := map[string]int{}
	var counts []toolCount
	var totalDur time.Duration
	var errors []toolRecord

	for _, rec := range tt.history {
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

	total := len(tt.history)
	fmt.Fprintf(&sb, "📎 %d tool", total)
	if total != 1 {
		sb.WriteByte('s')
	}
	sb.WriteString(" (")
	for i, tc := range counts {
		if i > 0 {
			sb.WriteString(", ")
		}
		emoji := emojiFor(tc.name)
		if tc.count > 1 {
			fmt.Fprintf(&sb, "%d× %s%s", tc.count, emoji, tc.name)
		} else {
			sb.WriteString(emoji + tc.name)
		}
	}
	sb.WriteString(") · ")
	sb.WriteString(channel.FormatDuration(totalDur))

	for _, rec := range errors {
		sb.WriteByte('\n')
		sb.WriteString(renderToolRecord(rec))
	}

	return sb.String()
}

// renderToolRecord formats a single completed tool record.
func renderToolRecord(rec toolRecord) string {
	statusEmoji := "✅"
	if rec.Status == "error" {
		statusEmoji = "❌"
	}
	emoji := emojiFor(rec.Tool)
	line := fmt.Sprintf("%s %s %s", statusEmoji, emoji, rec.Tool)
	if rec.Input != "" {
		input := truncate(rec.Input, 60)
		line += ": " + input
	}
	if rec.Detail != "" {
		detail := truncate(rec.Detail, 80)
		line += " → " + detail
	}
	line += fmt.Sprintf(" (%s)", channel.FormatDuration(rec.Duration))
	return line
}

// emojiFor returns the emoji for a tool name.
func emojiFor(tool string) string {
	if e, ok := toolEmoji[tool]; ok {
		return e
	}
	return toolEmoji["default"]
}

// truncate shortens s to maxLen bytes, appending "..." if truncated.
func truncate(s string, maxLen int) string {
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

// streamEvents consumes the agent event stream, accumulates text, and tracks tools.
// Returns the final response text, tool tracker, collected images, and any stream error.
func (b *Bot) streamEvents(msg WeixinMessage, events <-chan channel.Event) (string, *toolTracker, []channel.ImageEvent, error) {
	var sb strings.Builder
	var streamErr error
	var tt toolTracker
	var images []channel.ImageEvent

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if evt.Image != nil {
			images = append(images, *evt.Image)
			continue
		}

		if evt.ToolUse != nil {
			tt.handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)
	}

	return sb.String(), &tt, images, streamErr
}

// keepTyping sends typing indicators every 5 seconds until the context is cancelled.
// On cancel, it sends a stop-typing signal (status=2).
func (b *Bot) keepTyping(ctx context.Context, msg WeixinMessage) {
	ticket := b.getTypingTicket(msg.FromUserID)
	if ticket == "" {
		return
	}

	// Send initial typing indicator.
	if err := b.client.SendTyping(msg.FromUserID, ticket, 1, ""); err != nil {
		logger().Debug("typing start failed", "user_id", msg.FromUserID, "error", err)
	}

	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Send stop-typing.
			if err := b.client.SendTyping(msg.FromUserID, ticket, 2, ""); err != nil {
				logger().Debug("typing stop failed", "user_id", msg.FromUserID, "error", err)
			}
			return
		case <-ticker.C:
			if err := b.client.SendTyping(msg.FromUserID, ticket, 1, ""); err != nil {
				logger().Debug("typing refresh failed", "user_id", msg.FromUserID, "error", err)
			}
		}
	}
}

// getTypingTicket retrieves or fetches the typing_ticket for a user.
func (b *Bot) getTypingTicket(userID string) string {
	// Check cache first.
	if v, ok := b.typingTickets.Load(userID); ok {
		if ticket, ok := v.(string); ok && ticket != "" {
			return ticket
		}
	}

	// Fetch from API.
	contextToken := ""
	if v, ok := b.contextTokens.Load(userID); ok {
		contextToken, _ = v.(string)
	}

	resp, err := b.client.GetConfig(userID, contextToken, "")
	if err != nil {
		logger().Debug("getconfig for typing_ticket failed", "user_id", userID, "error", err)
		return ""
	}

	if resp.TypingTicket != "" {
		b.typingTickets.Store(userID, resp.TypingTicket)
	}

	return resp.TypingTicket
}
