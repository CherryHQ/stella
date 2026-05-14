package weixin

import (
	"context"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	// weixinMaxMessageLen is the maximum text message length for WeChat iLink.
	weixinMaxMessageLen = 2000

	// typingInterval is how often we re-send the typing indicator.
	// WeChat typing status expires after a few seconds.
	typingInterval = 5 * time.Second
)

const minToolDisplayDuration = 2 * time.Second

// newToolTracker creates a ToolTracker configured for WeChat display.
func newToolTracker() channel.ToolTracker {
	return channel.ToolTracker{MinDisplayDuration: minToolDisplayDuration}
}

// streamEvents consumes the agent event stream, accumulates text, and tracks tools.
// Returns the final response text, tool tracker, collected images, and any stream error.
func (b *Bot) streamEvents(msg WeixinMessage, events <-chan channel.Event) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
	var sb strings.Builder
	var streamErr error
	tt := newToolTracker()
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
			tt.Handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)
	}

	return sb.String(), &tt, images, streamErr
}

// keepTyping sends typing indicators every 5 seconds until the context is cancelled.
// On cancel, it sends a stop-typing signal (status=2).
func (b *Bot) keepTyping(ctx context.Context, msg WeixinMessage) {
	if b.guard.IsPaused() {
		return
	}
	ticket := b.getTypingTicket(msg.FromUserID)
	if ticket == "" {
		return
	}

	// Send initial typing indicator.
	if err := b.client.SendTyping(msg.FromUserID, ticket, 1); err != nil {
		logger().Debug("typing start failed", "user_id", msg.FromUserID, "error", err)
	}

	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Send stop-typing.
			if err := b.client.SendTyping(msg.FromUserID, ticket, 2); err != nil {
				logger().Debug("typing stop failed", "user_id", msg.FromUserID, "error", err)
			}
			return
		case <-ticker.C:
			if err := b.client.SendTyping(msg.FromUserID, ticket, 1); err != nil {
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

	resp, err := b.client.GetConfig(userID, contextToken)
	if err != nil {
		logger().Debug("getconfig for typing_ticket failed", "user_id", userID, "error", err)
		return ""
	}

	if resp.TypingTicket != "" {
		b.typingTickets.Store(userID, resp.TypingTicket)
	}

	return resp.TypingTicket
}
