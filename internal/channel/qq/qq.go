package qq

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"golang.org/x/oauth2"
)

const qqMaxMessageLen = 3500

func logger() *slog.Logger { return slog.With("component", "qq") }

// Config holds QQ Bot settings.
type Config struct {
	AppID      string
	AppSecret  string
	GroupMode  string   // "mention" | "always" | "disabled"
	AllowedIDs []string // user OpenIDs allowed (empty = allow all)
}

// Bot wraps a QQ bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	api            openapi.OpenAPI
	creds          *token.QQBotCredentials
	tokenSource    oauth2.TokenSource
	sessionManager botgo.SessionManager
	pool           *agent.Pool // fallback pool (default agent)
	poolManager    *agent.PoolManager
	store          config.Store
	agentCmd       *channel.AgentCommander
	cmd            *channel.Commander
	listFn         channel.ModelListFunc
	switchFn       channel.ModelSwitchFunc

	mu         sync.RWMutex
	chatModels map[string]channel.ModelOption

	allowed map[string]struct{}
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
}

// BotOption configures the QQ Bot.
type BotOption func(*Bot)

// WithPoolManager sets the pool manager for multi-agent routing.
func WithPoolManager(pm *agent.PoolManager) BotOption {
	return func(b *Bot) {
		b.poolManager = pm
	}
}

// WithStore sets the config store for user resolution and agent routing.
func WithStore(s config.Store) BotOption {
	return func(b *Bot) {
		b.store = s
		b.agentCmd = channel.NewAgentCommander(s)
	}
}

// New creates a QQ bot. Call Start to begin receiving events.
func New(cfg Config, pool *agent.Pool, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, opts ...BotOption) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("qq: app_id and app_secret are required")
	}

	if cfg.GroupMode == "" {
		cfg.GroupMode = "mention"
	}

	allowed := make(map[string]struct{}, len(cfg.AllowedIDs))
	for _, id := range cfg.AllowedIDs {
		allowed[id] = struct{}{}
	}

	poolAdapter := &channel.PoolAdapter[agent.SessionInfo]{
		ResolveFunc: func(ch string) (agent.SessionInfo, error) { return pool.ResolveSession(ch) },
		RotateFunc:  func(ch string) (agent.SessionInfo, error) { return pool.RotateSession(ch) },
		CompactFunc: pool.CompactSession,
		AdaptFn:     func(info agent.SessionInfo) channel.SessionInfo { return channel.SessionInfo{ID: info.ID} },
	}

	b := &Bot{
		pool:       pool,
		cmd:        channel.NewCommander(poolAdapter, listFn, switchFn),
		listFn:     listFn,
		switchFn:   switchFn,
		chatModels: make(map[string]channel.ModelOption),
		allowed:    allowed,
		cfg:        cfg,
	}

	for _, opt := range opts {
		opt(b)
	}

	return b, nil
}

// Start initializes the API client, registers event handlers, and starts
// a WebSocket connection. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	b.creds = &token.QQBotCredentials{
		AppID:     b.cfg.AppID,
		AppSecret: b.cfg.AppSecret,
	}

	b.tokenSource = token.NewQQBotTokenSource(b.creds)
	if err := token.StartRefreshAccessToken(b.ctx, b.tokenSource); err != nil {
		return fmt.Errorf("qq: start token refresh: %w", err)
	}

	b.api = botgo.NewOpenAPI(b.creds.AppID, b.tokenSource).WithTimeout(10 * time.Second)

	// Register event handlers and capture the intent bitmask.
	intent := event.RegisterHandlers(
		b.c2cMessageHandler(),
		b.groupATMessageHandler(),
	)

	// Get WebSocket endpoint.
	wsInfo, err := b.api.WS(b.ctx, nil, "")
	if err != nil {
		return fmt.Errorf("qq: get websocket info: %w", err)
	}

	logger().Info("websocket info", "shards", wsInfo.Shards)

	b.sessionManager = botgo.NewSessionManager()

	// Start WebSocket connection in a goroutine; block on context.
	go func() {
		if err := b.sessionManager.Start(wsInfo, b.tokenSource, &intent); err != nil {
			logger().Error("websocket session error", "error", err)
		}
	}()

	logger().Info("qq bot started (WebSocket mode)")

	<-b.ctx.Done()
	return b.ctx.Err()
}

// Stop cancels the bot context.
func (b *Bot) Stop() {
	logger().Info("stopping qq bot")
	if b.cancel != nil {
		b.cancel()
	}
}

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string { return "qq" }

// Notify sends a notification message. Implements channel.Channel.
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	if n.ChatID == "" {
		return fmt.Errorf("qq: no target chat ID")
	}

	msg := dto.MessageToCreate{
		Content: n.Text,
		MsgType: dto.TextMsg,
	}

	// Dispatch based on channel prefix to avoid unnecessary API errors.
	switch {
	case strings.HasPrefix(n.ChatID, "qq:group:"):
		targetID := strings.TrimPrefix(n.ChatID, "qq:group:")
		if _, err := b.api.PostGroupMessage(ctx, targetID, msg); err != nil {
			return fmt.Errorf("qq: group notify: %w", err)
		}
	default:
		// Treat as C2C; strip prefix if present.
		targetID := strings.TrimPrefix(n.ChatID, "qq:c2c:")
		if _, err := b.api.PostC2CMessage(ctx, targetID, msg); err != nil {
			return fmt.Errorf("qq: c2c notify: %w", err)
		}
	}

	return nil
}

// resolveAgentID returns the agent ID for the current context.
// Used by handlers to build session keys before full pool resolution.
func (b *Bot) resolveAgentID(authorID string) string {
	pool, _ := b.resolvePool(authorID)
	return pool.AgentID()
}

// isAllowed returns true if the sender is in the allowed list.
// An empty allowed list means everyone is allowed.
func (b *Bot) isAllowed(authorID string) bool {
	if len(b.allowed) == 0 {
		return true
	}
	_, ok := b.allowed[authorID]
	return ok
}

// channelForC2C returns the channel identifier for a C2C (private) chat.
func channelForC2C(userID string) string {
	return "qq:c2c:" + userID
}

// channelForGroup returns the channel identifier for a group chat.
func channelForGroup(groupID string) string {
	return "qq:group:" + groupID
}

// resolveSession returns the active session ID for the given message context,
// creating a new session if none exists.
func (b *Bot) resolveSession(authorID string, groupID string) (string, error) {
	pool, userID := b.resolvePool(authorID)
	sessionKey := b.buildSessionKey(authorID, groupID, pool.AgentID())
	info, err := pool.ResolveSession(sessionKey, userID)
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// buildSessionKey constructs a session key for the given QQ context.
func (b *Bot) buildSessionKey(authorID, groupID, agentID string) string {
	channelCtx := "private"
	if groupID != "" {
		channelCtx = "group:" + groupID
	}
	return agent.BuildSessionKey(agentID, "qq", authorID, channelCtx)
}

// resolvePool resolves the pool and user ID for the current message context.
// If poolManager and store are configured, it does: resolve user -> resolve agent -> get pool.
// Otherwise falls back to the default pool with userID 0.
func (b *Bot) resolvePool(authorID string) (*agent.Pool, int64) {
	if b.poolManager == nil || b.store == nil || authorID == "" {
		return b.pool, 0
	}

	ctx := context.Background()

	user, err := channel.ResolveUser(ctx, b.store, authorID, "qq", "")
	if err != nil {
		logger().Warn("resolve user failed, using default pool", "error", err)
		return b.pool, 0
	}

	chatCtx := channel.ChatContext{
		Platform: "qq",
	}

	agentID, err := channel.ResolveAgent(ctx, b.store, user, chatCtx)
	if err != nil {
		logger().Warn("resolve agent failed, using default pool", "error", err)
		return b.pool, user.ID
	}

	pool := b.poolManager.Get(agentID)
	if pool == nil {
		logger().Warn("agent pool not found, using default pool", "agent_id", agentID)
		return b.pool, user.ID
	}

	return pool, user.ID
}
