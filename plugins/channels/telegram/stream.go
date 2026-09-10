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

func (b *Bot) streamEventsChecked(c tele.Context, stream *channel.ChatStream) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
	return b.streamEventsWithCheck(c, stream, func(ctx context.Context) error {
		return stream.AuthorizeSend(ctx, channel.SendOutput)
	})
}

func (b *Bot) streamEventsWithCheck(c tele.Context, stream *channel.ChatStream, check func(context.Context) error) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
	if !isGroup(c) {
		text, tracker, images, fallback, err := b.streamDraftWithCheck(c, stream, check)
		if fallback {
			// Draft failed on first attempt — the event channel is still
			// open. Continue with edit-based streaming, preserving any
			// text and tool state already buffered from consumed events.
			logger().Info("sendMessageDraft not supported, falling back to edit mode")
			return b.streamEditEventsWithCheck(c, stream, text, tracker, images, check)
		}
		return text, tracker, images, err
	}
	return b.streamEditEventsWithCheck(c, stream, "", nil, nil, check)
}

func (b *Bot) streamDraftWithCheck(c tele.Context, stream *channel.ChatStream, check func(context.Context) error) (text string, tracker *channel.ToolTracker, images []channel.ImageEvent, fallback bool, err error) {
	var sb strings.Builder
	var streamErr error
	tt := newToolTracker()
	var imgs []channel.ImageEvent
	lastSend := time.Time{}
	// Kept inside int32 range so the identifier is valid on 32-bit builds too.
	draftID := rand.IntN(1<<31-1) + 1
	firstDraft := true

	for evt := range stream.Events {
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

		if check != nil {
			if err := check(b.ctx); err != nil {
				return sb.String(), &tt, imgs, false, err
			}
		}
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

func (b *Bot) streamEditEventsWithCheck(c tele.Context, stream *channel.ChatStream, initial string, existing *channel.ToolTracker, existingImages []channel.ImageEvent, check func(context.Context) error) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
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

	for evt := range stream.Events {
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
			if check != nil {
				if err := check(b.ctx); err != nil {
					return sb.String(), &tt, imgs, err
				}
			}
			msg, err := b.bot.Send(c.Chat(), display)
			if err != nil {
				logger().Warn("stream send failed", "error", err)
			} else {
				sentMsg = msg
			}
		} else {
			if check != nil {
				if err := check(b.ctx); err != nil {
					return sb.String(), &tt, imgs, err
				}
			}
			if _, err := b.bot.Edit(sentMsg, display); err != nil {
				logger().Warn("stream edit failed", "error", err)
			}
		}
		lastEdit = now
	}

	// Clean up the streaming message so the caller can send the final version.
	if sentMsg != nil {
		if check != nil {
			if err := check(b.ctx); err != nil {
				return sb.String(), &tt, imgs, err
			}
		}
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
