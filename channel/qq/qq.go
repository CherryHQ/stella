package qq

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/interaction/webhook"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"github.com/vaayne/anna/agent"
	"github.com/vaayne/anna/channel"
)

const qqMaxMessageLen = 3500

func logger() *slog.Logger { return slog.With("component", "qq") }

// Config holds QQ Bot settings.
type Config struct {
	AppID       string
	AppSecret   string
	NotifyChat  string // default user/group OpenID for notifications
	ListenAddr  string // webhook HTTP listen address (e.g. ":9000")
	WebhookPath string // webhook URL path (e.g. "/qqbot")
	Sandbox     bool
	GroupMode   string   // "mention" | "always" | "disabled"
	AllowedIDs  []string // user OpenIDs allowed (empty = allow all)
}

// Bot wraps a QQ bot with agent pool integration.
type Bot struct {
	api      openapi.OpenAPI
	creds    *token.QQBotCredentials
	pool     *agent.Pool
	listFn   ModelListFunc
	switchFn ModelSwitchFunc

	mu         sync.RWMutex
	chatModels map[string]ModelOption

	allowed map[string]struct{}
	cfg     Config
	ctx     context.Context
}

// New creates a QQ bot. Call Start to begin receiving events.
func New(cfg Config, pool *agent.Pool, listFn ModelListFunc, switchFn ModelSwitchFunc) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("qq: app_id and app_secret are required")
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":9000"
	}
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = "/qqbot"
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
// the webhook HTTP server. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx = ctx

	b.creds = &token.QQBotCredentials{
		AppID:     b.cfg.AppID,
		AppSecret: b.cfg.AppSecret,
	}

	tokenSource := token.NewQQBotTokenSource(b.creds)
	if err := token.StartRefreshAccessToken(ctx, tokenSource); err != nil {
		return fmt.Errorf("qq: start token refresh: %w", err)
	}

	if b.cfg.Sandbox {
		b.api = botgo.NewSandboxOpenAPI(b.creds.AppID, tokenSource).WithTimeout(10 * time.Second)
	} else {
		b.api = botgo.NewOpenAPI(b.creds.AppID, tokenSource).WithTimeout(10 * time.Second)
	}

	b.registerHandlers()

	mux := http.NewServeMux()
	mux.HandleFunc(b.cfg.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
		webhook.HTTPHandler(w, r, b.creds)
	})

	server := &http.Server{
		Addr:              b.cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger().Info("webhook server starting", "addr", b.cfg.ListenAddr, "path", b.cfg.WebhookPath)

	go func() {
		<-ctx.Done()
		logger().Info("webhook server stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("qq: webhook server: %w", err)
	}
	return ctx.Err()
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

	// Try C2C first; if chatID starts with a group prefix convention, use group API.
	if _, err := b.api.PostC2CMessage(ctx, chatID, msg); err != nil {
		logger().Warn("c2c notify failed, trying group", "error", err)
		if _, err := b.api.PostGroupMessage(ctx, chatID, msg); err != nil {
			return fmt.Errorf("qq: notify: %w", err)
		}
	}

	logger().Debug("notification sent", "chat_id", chatID)
	return nil
}

// registerHandlers wires event handlers for QQ messages.
func (b *Bot) registerHandlers() {
	_ = event.RegisterHandlers(
		b.c2cMessageHandler(),
		b.groupATMessageHandler(),
	)
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
	return "qq" + userID
}

// channelForGroup returns the channel identifier for a group chat.
func channelForGroup(groupID string) string {
	return "qqg" + groupID
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
