package telegram

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
	tele "gopkg.in/telebot.v4"
)

// streamEditInterval controls how often we edit the message during streaming.
const streamEditInterval = time.Second

// typingInterval is how often we re-send the typing indicator. Telegram
// expires typing status after ~5 seconds, so we resend every 4s.
const typingInterval = 4 * time.Second

// typingCursor is appended to the message while streaming to indicate activity.
const typingCursor = " \u258D"

// minToolDisplayDuration is the minimum time a tool indicator stays visible
// after completion, so it doesn't flash and disappear instantly.
const minToolDisplayDuration = 2 * time.Second

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
	displayUntil time.Time // minimum time to keep showing the last-finished tool
}

// start registers a new tool as running.
func (tt *toolTracker) start(t *runner.ToolUseEvent) {
	// If a tool was already active, finish it as "done" (missed the finish event).
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

// finish records the active tool as completed and enforces minimum display time.
func (tt *toolTracker) finish(t *runner.ToolUseEvent) {
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
	tt.displayUntil = time.Now().Add(minToolDisplayDuration)
	tt.activeTool = ""
	tt.activeInput = ""
	tt.activeStart = time.Time{}
}

// handle processes a tool event, returning true if a display refresh is needed.
func (tt *toolTracker) handle(t *runner.ToolUseEvent) bool {
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

// render builds the tool summary section for the streaming display.
func (tt *toolTracker) render() string {
	var sb strings.Builder

	// Completed tools summary.
	for _, rec := range tt.history {
		sb.WriteString(renderToolRecord(rec))
		sb.WriteByte('\n')
	}

	// Active tool with spinner.
	if tt.activeTool != "" {
		emoji := emojiFor(tt.activeTool)
		elapsed := time.Since(tt.activeStart).Truncate(100 * time.Millisecond)
		line := fmt.Sprintf("⏳ %s %s", emoji, tt.activeTool)
		if tt.activeInput != "" {
			input := truncate(tt.activeInput, 60)
			line += ": " + input
		}
		line += fmt.Sprintf(" (%s)", channel.FormatDuration(elapsed))
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	return sb.String()
}

// isDisplaying returns true if there is an active tool or the minimum display
// duration for the last finished tool has not yet elapsed.
func (tt *toolTracker) isDisplaying() bool {
	if tt.activeTool != "" {
		return true
	}
	return time.Now().Before(tt.displayUntil)
}

// hasHistory returns true if any tools were tracked.
func (tt *toolTracker) hasHistory() bool {
	return len(tt.history) > 0
}

// renderFinal builds a compact tool summary for the final message.
// Shows a one-liner with tool counts and total time, plus individual
// lines for any failed tool calls.
func (tt *toolTracker) renderFinal() string {
	if len(tt.history) == 0 {
		return ""
	}

	// Count tools by name (preserving order of first appearance).
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

	// Build compact summary: "📎 5 tools (2× read, 2× bash, 1× edit) · 3.2s"
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

	// Append individual lines for error calls.
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
// Cuts at a valid UTF-8 boundary to avoid producing invalid strings.
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

// streamResponse consumes the agent stream, displaying progress in real time.
// For private chats it uses Telegram's sendMessageDraft API (Bot API 9.3+)
// for smooth animated streaming. For groups (where drafts aren't supported)
// it falls back to the edit-in-place approach.
func (b *Bot) streamResponse(c tele.Context, sessionID string, prompt runner.MessageContent) (string, *toolTracker, []runner.ImageEvent, error) {
	pool, _ := b.resolvePool(c)
	events := pool.Chat(b.ctx, sessionID, prompt)

	if !isGroup(c) {
		text, tracker, images, fallback, err := b.streamDraft(c, events)
		if fallback {
			// Draft failed on first attempt — the event channel is still
			// open. Continue with edit-based streaming, preserving any
			// text and tool state already buffered from consumed events.
			logger().Info("sendMessageDraft not supported, falling back to edit mode")
			return b.streamEditEvents(c, events, text, tracker, images)
		}
		return text, tracker, images, err
	}
	return b.streamEditEvents(c, events, "", nil, nil)
}

// streamDraft uses Telegram's sendMessageDraft API for smooth streaming
// in private chats. If the first draft call fails, it returns fallback=true
// so the caller can switch to edit mode. The buffered text is returned so
// no consumed events are lost.
func (b *Bot) streamDraft(c tele.Context, events <-chan runner.Event) (text string, tracker *toolTracker, images []runner.ImageEvent, fallback bool, err error) {
	var sb strings.Builder
	var streamErr error
	var tt toolTracker
	var imgs []runner.ImageEvent
	lastSend := time.Time{}
	draftID := rand.Int64N(1<<53) + 1
	chatID := c.Chat().ID
	firstDraft := true

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if evt.Image != nil {
			imgs = append(imgs, *evt.Image)
			continue
		}

		forceRefresh := false
		if evt.ToolUse != nil {
			forceRefresh = tt.handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)

		now := time.Now()
		if !forceRefresh && now.Sub(lastSend) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && !tt.isDisplaying() {
			continue
		}

		display := buildStreamDisplay(current, tt.render(), tt.isDisplaying())

		if err := b.sendDraftRaw(chatID, draftID, display); err != nil {
			if firstDraft {
				return sb.String(), &tt, imgs, true, nil
			}
			logger().Warn("sendMessageDraft failed mid-stream", "error", err)
		}
		firstDraft = false
		lastSend = now
	}

	return sb.String(), &tt, imgs, false, streamErr
}

// streamEditEvents uses the traditional edit-in-place approach for streaming,
// consuming from an existing event channel. Required for group chats where
// sendMessageDraft is not available. Any already-buffered text from a prior
// draft attempt is preserved via the initial parameter.
func (b *Bot) streamEditEvents(c tele.Context, events <-chan runner.Event, initial string, existing *toolTracker, existingImages []runner.ImageEvent) (string, *toolTracker, []runner.ImageEvent, error) {
	var sb strings.Builder
	sb.WriteString(initial)
	var sentMsg *tele.Message
	var streamErr error
	var tt toolTracker
	if existing != nil {
		tt = *existing
	}
	var imgs []runner.ImageEvent
	imgs = append(imgs, existingImages...)
	lastEdit := time.Time{}

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if evt.Image != nil {
			imgs = append(imgs, *evt.Image)
			continue
		}

		forceRefresh := false
		if evt.ToolUse != nil {
			forceRefresh = tt.handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)

		now := time.Now()
		if !forceRefresh && now.Sub(lastEdit) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && !tt.isDisplaying() {
			continue
		}

		display := buildStreamDisplay(current, tt.render(), tt.isDisplaying())

		if sentMsg == nil {
			msg, err := b.bot.Send(c.Chat(), display)
			if err != nil {
				logger().Warn("stream send failed", "error", err)
			} else {
				sentMsg = msg
			}
		} else {
			if _, err := b.bot.Edit(sentMsg, display); err != nil {
				logger().Warn("stream edit failed", "error", err)
			}
		}
		lastEdit = now
	}

	// Clean up the streaming message so the caller can send the final version.
	if sentMsg != nil {
		if err := b.bot.Delete(sentMsg); err != nil {
			logger().Warn("delete streaming message failed", "error", err)
		}
	}

	return sb.String(), &tt, imgs, streamErr
}

// buildStreamDisplay constructs the streaming display text with tool summary,
// cursor, and length truncation (UTF-8 safe).
func buildStreamDisplay(text, toolSection string, hasTools bool) string {
	display := text
	suffix := typingCursor

	if hasTools && toolSection != "" {
		suffix = "\n\n" + strings.TrimRight(toolSection, "\n") + typingCursor
	}

	if len(suffix) >= telegramMaxMessageLen {
		suffix = typingCursor
	}

	if len(display)+len(suffix) > telegramMaxMessageLen {
		cutAt := telegramMaxMessageLen - len(suffix) - 3
		if cutAt < 0 {
			cutAt = 0
		}
		for cutAt > 0 && !utf8.RuneStart(display[cutAt]) {
			cutAt--
		}
		display = display[:cutAt] + "..."
	}

	return display + suffix
}

// sendDraftRaw calls the Telegram sendMessageDraft API (Bot API 9.3+).
// This provides smooth animated streaming in private chats without
// the rate-limiting issues of repeated editMessageText calls.
func (b *Bot) sendDraftRaw(chatID, draftID int64, text string) error {
	params := map[string]string{
		"chat_id":  strconv.FormatInt(chatID, 10),
		"draft_id": strconv.FormatInt(draftID, 10),
		"text":     text,
	}
	_, err := b.bot.Raw("sendMessageDraft", params)
	return err
}

// keepTyping sends the typing indicator repeatedly until ctx is cancelled.
func keepTyping(ctx context.Context, c tele.Context) {
	_ = c.Notify(tele.Typing)
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Notify(tele.Typing)
		}
	}
}
