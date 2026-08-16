package telegram

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
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

// newToolTracker creates a ToolTracker configured for Telegram display.
func newToolTracker() channel.ToolTracker {
	return channel.ToolTracker{MinDisplayDuration: minToolDisplayDuration}
}

// streamEvents consumes the agent event stream, displaying progress in real time.
// For private chats it uses Telegram's sendMessageDraft API (Bot API 9.3+)
// for smooth animated streaming. For groups (where drafts aren't supported)
// it falls back to the edit-in-place approach.
func (b *Bot) streamEvents(c tele.Context, events <-chan channel.Event) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
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
func (b *Bot) streamDraft(c tele.Context, events <-chan channel.Event) (text string, tracker *channel.ToolTracker, images []channel.ImageEvent, fallback bool, err error) {
	var sb strings.Builder
	var streamErr error
	tt := newToolTracker()
	var imgs []channel.ImageEvent
	lastSend := time.Time{}
	// Kept inside int32 range so the identifier is valid on 32-bit builds too.
	draftID := rand.IntN(1<<31-1) + 1
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
			forceRefresh = tt.Handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)

		now := time.Now()
		if !forceRefresh && now.Sub(lastSend) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && !tt.IsDisplaying() {
			continue
		}

		display := buildStreamDisplay(current, tt.Render(), tt.IsDisplaying())

		if err := b.bot.SendDraft(c.Chat(), draftID, display); err != nil {
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
func (b *Bot) streamEditEvents(c tele.Context, events <-chan channel.Event, initial string, existing *channel.ToolTracker, existingImages []channel.ImageEvent) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
	var sb strings.Builder
	sb.WriteString(initial)
	var sentMsg *tele.Message
	var streamErr error
	tt := newToolTracker()
	if existing != nil {
		tt = *existing
	}
	var imgs []channel.ImageEvent
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
			forceRefresh = tt.Handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)

		now := time.Now()
		if !forceRefresh && now.Sub(lastEdit) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && !tt.IsDisplaying() {
			continue
		}

		display := buildStreamDisplay(current, tt.Render(), tt.IsDisplaying())

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
		cutAt := max(telegramMaxMessageLen-len(suffix)-3, 0)
		for cutAt > 0 && !utf8.RuneStart(display[cutAt]) {
			cutAt--
		}
		display = display[:cutAt] + "..."
	}

	return display + suffix
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
