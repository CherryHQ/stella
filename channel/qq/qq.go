package qq

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"github.com/vaayne/anna/agent"
	"github.com/vaayne/anna/channel"
	"golang.org/x/oauth2"
)

const qqMaxMessageLen = 3500

func logger() *slog.Logger { return slog.With("component", "qq") }

// Config holds QQ Bot settings.
type Config struct {
	AppID      string
	AppSecret  string
	NotifyChat string // default user/group OpenID for notifications
	Sandbox    bool
	GroupMode  string   // "mention" | "always" | "disabled"
	AllowedIDs []string // user OpenIDs allowed (empty = allow all)
}

// Bot wraps a QQ bot with agent pool integration.
type Bot struct {
	api            openapi.OpenAPI
	creds          *token.QQBotCredentials
	tokenSource    oauth2.TokenSource
	sessionManager botgo.SessionManager
	pool           *agent.Pool
	listFn         ModelListFunc
	switchFn       ModelSwitchFunc

	mu         sync.RWMutex
	chatModels map[string]ModelOption

	allowed map[string]struct{}
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
}

// New creates a QQ bot. Call Start to begin receiving events.
func New(cfg Config, pool *agent.Pool, listFn ModelListFunc, switchFn ModelSwitchFunc) (*Bot, error) {
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
		pool:       pool,
		listFn:     listFn,
		switchFn:   switchFn,
		chatModels: make(map[string]ModelOption),
		allowed:    allowed,
		cfg:        cfg,
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

	if b.cfg.Sandbox {
		b.api = botgo.NewSandboxOpenAPI(b.creds.AppID, b.tokenSource).WithTimeout(10 * time.Second)
	} else {
		b.api = botgo.NewOpenAPI(b.creds.AppID, b.tokenSource).WithTimeout(10 * time.Second)
	}

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

// Name returns the backend name. Implements channel.Backend.
func (b *Bot) Name() string { return "qq" }

// Notify sends a notification message. Implements channel.Backend.
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		chatID = b.cfg.NotifyChat
	}
	if chatID == "" {
		return fmt.Errorf("qq: no target chat ID")
	}

	logger().Debug("sending notification", "chat_id", chatID, "text_len", len(n.Text))

	msg := dto.MessageToCreate{
		Content: n.Text,
		MsgType: dto.TextMsg,
	}

	// Try C2C first; if that fails, try group API.
	if _, err := b.api.PostC2CMessage(ctx, chatID, msg); err != nil {
		logger().Warn("c2c notify failed, trying group", "error", err)
		if _, err := b.api.PostGroupMessage(ctx, chatID, msg); err != nil {
			return fmt.Errorf("qq: notify: %w", err)
		}
	}

	logger().Debug("notification sent", "chat_id", chatID)
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

// resolveSession returns the active session ID for the given channel,
// creating a new session if none exists.
func (b *Bot) resolveSession(ch string) (string, error) {
	info, err := b.pool.ResolveSession(ch)
	if err != nil {
		return "", err
	}
	return info.ID, nil
}
