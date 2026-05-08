package weixin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

// Config holds WeChat iLink bot settings.
type Config struct {
	InstanceID string `json:"-"`
	BotToken   string `json:"bot_token"` // iLink bot_token
	BaseURL    string `json:"base_url"`  // iLink base URL (default: https://ilinkai.weixin.qq.com)
	BotID      string `json:"bot_id"`    // ilink_bot_id
	UserID     string `json:"user_id"`   // ilink_user_id
}

// Bot wraps a WeChat iLink bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	client  *Client
	handler channel.Handler

	contextTokens sync.Map // key: userID string, value: contextToken string
	typingTickets sync.Map // key: userID string, value: typingTicket string

	// cursor holds the getupdates cursor in memory. It is lost on restart
	// but repopulated immediately on the first getupdates response.
	cursor string

	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a WeChat iLink bot. Call Start to begin polling.
func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("weixin: bot_token is required")
	}

	b := &Bot{
		handler: handler,
		cfg:     cfg,
	}

	return b, nil
}

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformWeixin
}

func (b *Bot) Platform() string { return channel.PlatformWeixin }

// Stop gracefully shuts down the WeChat bot. Implements channel.Channel.
func (b *Bot) Stop() {
	logger().Info("stopping weixin bot")
	if b.cancel != nil {
		b.cancel()
	}
}

// Start begins long-polling for messages. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Create the client with config values.
	b.client = NewClient(b.cfg.BaseURL, "", b.cfg.BotToken)

	// Poll loop with retry/backoff.
	timeout := 35 * time.Second
	consecutiveFailures := 0

	logger().Info("polling started")

	for {
		select {
		case <-b.ctx.Done():
			logger().Info("polling stopped")
			return b.ctx.Err()
		default:
		}

		resp, err := b.client.GetUpdates(b.cursor, "", timeout)
		if err != nil {
			if errors.Is(err, ErrSessionExpired) {
				logger().Error("session expired, clearing all state")
				b.clearState()
				return fmt.Errorf("weixin: session expired (ret=-14)")
			}

			// Local timeout — treat as empty response, continue.
			if isTimeoutError(err) {
				consecutiveFailures = 0
				continue
			}

			consecutiveFailures++
			wait := 2 * time.Second
			if consecutiveFailures >= 3 {
				wait = 30 * time.Second
			}
			logger().Warn("getupdates failed, retrying",
				"error", err, "failures", consecutiveFailures, "wait", wait)

			select {
			case <-b.ctx.Done():
				return b.ctx.Err()
			case <-time.After(wait):
				continue
			}
		}

		// Success — reset failure counter.
		consecutiveFailures = 0

		// Update cursor.
		if resp.GetUpdatesBuf != "" {
			b.cursor = resp.GetUpdatesBuf
		}

		// Use adaptive timeout from response.
		if resp.LongPollingTimeoutMS > 0 {
			timeout = time.Duration(resp.LongPollingTimeoutMS) * time.Millisecond
		}

		// Dispatch messages.
		if len(resp.Msgs) > 0 {
			b.handleUpdates(resp.Msgs)
		}
	}
}

// Notify sends a notification message via sendmessage. Implements channel.Channel.
func (b *Bot) Notify(_ context.Context, n channel.Notification) error {
	if b.client == nil {
		return fmt.Errorf("weixin: bot not started")
	}

	targetUser := n.ChatID
	if targetUser == "" {
		return fmt.Errorf("weixin: no target user ID for notification")
	}

	// Look up cached context_token for the target user.
	tokenVal, ok := b.contextTokens.Load(targetUser)
	if !ok {
		return fmt.Errorf("weixin: no context_token for user %s (tokens are in-memory only, repopulated when user sends a message)", targetUser)
	}
	contextToken, _ := tokenVal.(string)

	msg := WeixinMessage{
		ToUserID:     targetUser,
		ClientID:     RandomClientID("notify"),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList: []MessageItem{
			{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: n.Text},
			},
		},
	}

	if err := b.client.SendMessage(msg, ""); err != nil {
		return fmt.Errorf("weixin: send notification: %w", err)
	}
	return nil
}

// clearState clears in-memory caches on session expiry.
func (b *Bot) clearState() {
	b.contextTokens = sync.Map{}
	b.typingTickets = sync.Map{}
	b.cursor = ""
}

// isTimeoutError checks if an error is a network timeout.
func isTimeoutError(err error) bool {
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}
