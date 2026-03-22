package weixin

import (
	"context"
	"fmt"
	"sync"

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
	mu      sync.RWMutex
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
