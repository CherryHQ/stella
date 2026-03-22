package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
)

// Config holds WeChat iLink bot settings.
type Config struct {
	BotToken   string   `json:"bot_token"`   // iLink bot_token
	BaseURL    string   `json:"base_url"`    // iLink base URL (default: https://ilinkai.weixin.qq.com)
	BotID      string   `json:"bot_id"`      // ilink_bot_id
	UserID     string   `json:"user_id"`     // ilink_user_id
	NotifyChat string   `json:"notify_chat"` // default user ID for notifications (requires context_token)
	AllowedIDs []string `json:"allowed_ids"` // user IDs allowed (empty = allow all)
}

// dbConfig is the JSON shape persisted in settings_channels.config.
// It extends Config with runtime state fields.
type dbConfig struct {
	Config
	GetUpdatesBuf string `json:"get_updates_buf,omitempty"`
}

// Bot wraps a WeChat iLink bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	client      *Client
	poolManager *agent.PoolManager
	store       config.Store
	authStore   auth.AuthStore
	engine      *auth.PolicyEngine
	linkCodes   *auth.LinkCodeStore
	agentCmd    *channel.AgentCommander
	listFn      channel.ModelListFunc
	switchFn    channel.ModelSwitchFunc

	contextTokens sync.Map // key: userID string, value: contextToken string
	typingTickets sync.Map // key: userID string, value: typingTicket string

	allowed map[string]struct{} // empty map = allow all
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
}

// BotOption configures the WeChat Bot.
type BotOption func(*Bot)

// WithAuth configures the bot with auth store and link code store for
// account linking support.
func WithAuth(authStore auth.AuthStore, engine *auth.PolicyEngine, linkCodes *auth.LinkCodeStore) BotOption {
	return func(b *Bot) {
		b.authStore = authStore
		b.engine = engine
		b.linkCodes = linkCodes
		b.agentCmd = channel.NewAgentCommander(b.store, authStore)
	}
}

// New creates a WeChat iLink bot. Call Start to begin polling.
func New(cfg Config, pm *agent.PoolManager, store config.Store, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, opts ...BotOption) (*Bot, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("weixin: bot_token is required")
	}

	allowed := make(map[string]struct{}, len(cfg.AllowedIDs))
	for _, id := range cfg.AllowedIDs {
		allowed[id] = struct{}{}
	}

	b := &Bot{
		poolManager: pm,
		store:       store,
		agentCmd:    channel.NewAgentCommander(store, nil),
		listFn:      listFn,
		switchFn:    switchFn,
		allowed:     allowed,
		cfg:         cfg,
	}

	for _, opt := range opts {
		opt(b)
	}

	return b, nil
}

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string { return "weixin" }

// Stop gracefully shuts down the WeChat bot. Implements channel.Channel.
func (b *Bot) Stop() {
	logger().Info("stopping weixin bot")
	if b.cancel != nil {
		b.cancel()
	}
}

// isAllowed returns true if the user is in the allowed list.
// An empty allowed list means everyone is allowed.
func (b *Bot) isAllowed(userID string) bool {
	if len(b.allowed) == 0 {
		return true
	}
	_, ok := b.allowed[userID]
	return ok
}

// Start begins long-polling for messages. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Create the client with config values.
	b.client = NewClient(b.cfg.BaseURL, "", b.cfg.BotToken)

	// Load saved cursor from DB channel config.
	buf := b.loadCursor()

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

		resp, err := b.client.GetUpdates(buf, "", timeout)
		if err != nil {
			if errors.Is(err, ErrSessionExpired) {
				logger().Error("session expired, clearing all state")
				b.clearCredentials()
				return fmt.Errorf("weixin: session expired (ret=-14), credentials cleared")
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

		// Update cursor and persist.
		if resp.GetUpdatesBuf != "" {
			buf = resp.GetUpdatesBuf
			b.persistCursor(buf)
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

// handleUpdates is a placeholder for Phase 3 message handling.
func (b *Bot) handleUpdates(msgs []WeixinMessage) {
	logger().Info("received messages", "count", len(msgs))
}

// Notify sends a notification message via sendmessage. Implements channel.Channel.
func (b *Bot) Notify(_ context.Context, n channel.Notification) error {
	if b.client == nil {
		return fmt.Errorf("weixin: bot not started")
	}

	targetUser := n.ChatID
	if targetUser == "" {
		targetUser = b.cfg.NotifyChat
	}
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

// resolve performs full user/agent/pool/session-key resolution.
func (b *Bot) resolve(userID string) (*channel.ResolvedChat, error) {
	return channel.Resolve(
		context.Background(),
		b.poolManager,
		b.store,
		b.authStore,
		b.engine,
		"weixin",
		userID,
		"",     // no display name available from iLink
		userID, // chatID = userID for DM
		false,  // DM only for v1
	)
}

// loadCursor loads the get_updates_buf cursor from the DB channel config.
func (b *Bot) loadCursor() string {
	ch, err := b.store.GetChannel(context.Background(), "weixin")
	if err != nil {
		return ""
	}
	var dc dbConfig
	if err := json.Unmarshal([]byte(ch.Config), &dc); err != nil {
		return ""
	}
	return dc.GetUpdatesBuf
}

// persistCursor saves the get_updates_buf cursor to the DB channel config.
func (b *Bot) persistCursor(buf string) {
	ch, err := b.store.GetChannel(context.Background(), "weixin")
	if err != nil {
		logger().Warn("failed to load channel config for cursor persist", "error", err)
		return
	}

	// Merge into existing JSON config.
	var raw map[string]any
	if err := json.Unmarshal([]byte(ch.Config), &raw); err != nil {
		raw = make(map[string]any)
	}
	raw["get_updates_buf"] = buf

	data, err := json.Marshal(raw)
	if err != nil {
		logger().Warn("failed to marshal cursor update", "error", err)
		return
	}

	if err := b.store.UpsertChannel(context.Background(), config.Channel{
		ID:      "weixin",
		Enabled: ch.Enabled,
		Config:  string(data),
	}); err != nil {
		logger().Warn("failed to persist cursor", "error", err)
	}
}

// clearCredentials removes credentials and state from DB and in-memory caches.
func (b *Bot) clearCredentials() {
	// Clear in-memory caches.
	b.contextTokens = sync.Map{}
	b.typingTickets = sync.Map{}

	// Clear credentials from DB.
	ch, err := b.store.GetChannel(context.Background(), "weixin")
	if err != nil {
		logger().Warn("failed to load channel config for credential clear", "error", err)
		return
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(ch.Config), &raw); err != nil {
		raw = make(map[string]any)
	}

	delete(raw, "bot_token")
	delete(raw, "bot_id")
	delete(raw, "user_id")
	delete(raw, "get_updates_buf")

	data, err := json.Marshal(raw)
	if err != nil {
		logger().Warn("failed to marshal credential clear", "error", err)
		return
	}

	if err := b.store.UpsertChannel(context.Background(), config.Channel{
		ID:      "weixin",
		Enabled: ch.Enabled,
		Config:  string(data),
	}); err != nil {
		logger().Warn("failed to clear credentials in DB", "error", err)
	}
}

// isTimeoutError checks if an error is a network timeout.
func isTimeoutError(err error) bool {
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}
