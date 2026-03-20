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
	"github.com/vaayne/anna/internal/auth"
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
	poolManager    *agent.PoolManager
	store          config.Store
	authStore      auth.AuthStore
	linkCodes      *auth.LinkCodeStore
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

// WithAuth configures the bot with auth store and link code store for
// account linking support.
func WithAuth(authStore auth.AuthStore, linkCodes *auth.LinkCodeStore) BotOption {
	return func(b *Bot) {
		b.authStore = authStore
		b.linkCodes = linkCodes
	}
}

// New creates a QQ bot. Call Start to begin receiving events.
func New(cfg Config, pm *agent.PoolManager, store config.Store, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, opts ...BotOption) (*Bot, error) {
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

	b := &Bot{
		poolManager: pm,
		store:       store,
		listFn:      listFn,
		switchFn:    switchFn,
		chatModels:  make(map[string]channel.ModelOption),
		allowed:     allowed,
		cfg:         cfg,
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

// resolve performs full user/agent/pool/session-key resolution for the
// given QQ message context. Call once per incoming message or command.
func (b *Bot) resolve(authorID, groupID string) (*channel.ResolvedChat, error) {
	return channel.Resolve(
		context.Background(),
		b.poolManager,
		b.store,
		"qq",
		authorID,
		"",
		groupID,
		groupID != "",
	)
}
