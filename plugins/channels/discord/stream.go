package discord

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// streamEditInterval controls how often we edit the message during streaming.
// Discord's rate limiter is handled internally by discordgo; >=1s is polite
// throttling, not crash prevention.
const streamEditInterval = time.Second

// typingInterval is how often we re-send the typing indicator. Discord typing
// expires after ~10s, so we resend every 8s.
const typingInterval = 8 * time.Second

// typingCursor is appended to the message while streaming to indicate activity.
const typingCursor = " ▍"

// minToolDisplayDuration is the minimum time a tool indicator stays visible
// after completion, so it doesn't flash and disappear instantly.
const minToolDisplayDuration = 2 * time.Second

// newToolTracker creates a ToolTracker configured for Discord display.
func newToolTracker() channel.ToolTracker {
	return channel.ToolTracker{MinDisplayDuration: minToolDisplayDuration}
}

// streamEvents consumes the agent event stream, editing a single message in
// place to show progress. Discord has no draft API, so edit-in-place is the only
// streaming mode.
func (b *Bot) streamEvents(chatID string, events <-chan channel.Event) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
	var sb strings.Builder
	var sentMsgID string
	var streamErr error
	tt := newToolTracker()
	var imgs []channel.ImageEvent
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

		if sentMsgID == "" {
			msg, err := b.session.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{
				Content:         display,
				AllowedMentions: suppressMentions(),
			})
			if err != nil {
				logger().Warn("stream send failed", "error", err)
			} else {
				sentMsgID = msg.ID
			}
		} else {
			// Edit through the complex API so mentions stay suppressed (H1): an
			// omitted allowed_mentions on edit would let a mid-stream @everyone in
			// the agent's partial text ping the whole server.
			if _, err := b.session.ChannelMessageEditComplex(&discordgo.MessageEdit{
				Channel:         chatID,
				ID:              sentMsgID,
				Content:         &display,
				AllowedMentions: suppressMentions(),
			}); err != nil {
				logger().Warn("stream edit failed", "error", err)
			}
		}
		lastEdit = now
	}

	// Clean up the streaming message so the caller can send the final version.
	if sentMsgID != "" {
		if err := b.session.ChannelMessageDelete(chatID, sentMsgID); err != nil {
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

	// When the suffix alone leaves no room for the "..." ellipsis, drop it to the
	// bare cursor. Without this, a near-limit suffix (e.g. len 1998) yields
	// cutAt==0 and "..."+suffix overflows discordMaxMessageLen.
	if len(suffix)+len("...") > discordMaxMessageLen {
		suffix = typingCursor
	}

	if len(display)+len(suffix) > discordMaxMessageLen {
		cutAt := max(discordMaxMessageLen-len(suffix)-3, 0)
		for cutAt > 0 && !utf8.RuneStart(display[cutAt]) {
			cutAt--
		}
		display = display[:cutAt] + "..."
	}

	return display + suffix
}

// keepTyping sends the typing indicator repeatedly until ctx is cancelled.
func (b *Bot) keepTyping(ctx context.Context, chatID string) {
	_ = b.session.ChannelTyping(chatID)
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = b.session.ChannelTyping(chatID)
		}
	}
}
