package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/vaayne/anna/agent"
	"github.com/vaayne/anna/channel"
)

const feishuMaxMessageLen = 4000

func logger() *slog.Logger { return slog.With("component", "feishu") }

// Config holds Feishu bot settings.
type Config struct {
	AppID             string   // app ID
	AppSecret         string   // app secret
	EncryptKey        string   // event encrypt key (from Feishu developer console)
	VerificationToken string   // event verification token (from Feishu developer console)
	NotifyChat        string   // default chat ID for proactive notifications
	GroupMode         string   // "mention" | "always" | "disabled"
	AllowedIDs        []string // user open_ids allowed (empty = allow all)
}

// Bot wraps a Feishu bot with agent pool integration.
type Bot struct {
	client   *lark.Client
	wsClient *larkws.Client
	pool     *agent.Pool
	listFn   ModelListFunc
	switchFn ModelSwitchFunc

	mu         sync.RWMutex
	chatModels map[string]ModelOption

	allowed map[string]struct{}
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
}

// New creates a Feishu bot. Call Start to begin receiving events.
func New(cfg Config, pool *agent.Pool, listFn ModelListFunc, switchFn ModelSwitchFunc) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("feishu: app_id and app_secret are required")
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

	b.client = lark.NewClient(b.cfg.AppID, b.cfg.AppSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo),
		lark.WithEnableTokenCache(true),
	)

	eventHandler := dispatcher.NewEventDispatcher(b.cfg.VerificationToken, b.cfg.EncryptKey).
		OnP2MessageReceiveV1(b.onMessage)

	b.wsClient = larkws.NewClient(b.cfg.AppID, b.cfg.AppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	logger().Info("feishu bot starting (WebSocket mode)")

	errCh := make(chan error, 1)
	go func() {
		errCh <- b.wsClient.Start(b.ctx)
	}()

	select {
	case <-b.ctx.Done():
		return b.ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("feishu: websocket error: %w", err)
	}
}

// Stop cancels the bot context.
func (b *Bot) Stop() {
	logger().Info("stopping feishu bot")
	if b.cancel != nil {
		b.cancel()
	}
}

// Name returns the backend name. Implements channel.Backend.
func (b *Bot) Name() string { return "feishu" }

// Notify sends a notification message. Implements channel.Backend.
func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		chatID = b.cfg.NotifyChat
	}
	if chatID == "" {
		return fmt.Errorf("feishu: no target chat ID")
	}

	// Strip channel prefix if present.
	chatID = strings.TrimPrefix(chatID, "feishu:")

	content := textContent(n.Text)
	resp, err := b.client.Im.Message.Create(ctx,
		larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				ReceiveId(chatID).
				Content(content).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("feishu: notify: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu: notify: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// isAllowed returns true if the sender is in the allowed list.
// An empty allowed list means everyone is allowed.
func (b *Bot) isAllowed(openID string) bool {
	if len(b.allowed) == 0 {
		return true
	}
	_, ok := b.allowed[openID]
	return ok
}

// channelForChat returns the channel identifier for a Feishu chat.
func channelForChat(chatID string) string {
	return "feishu:" + chatID
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

// textContent builds the JSON content string for a Feishu text message.
func textContent(text string) string {
	data, _ := json.Marshal(map[string]string{"text": text})
	return string(data)
}

// cardContent builds a Feishu Interactive Card JSON 2.0 string with markdown content.
// Cards support the Patch API for in-place editing (plain text messages do not).
func cardContent(text string) string {
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": text,
				},
			},
		},
	}
	data, _ := json.Marshal(card)
	return string(data)
}

// shouldRespondInGroup checks whether the bot should respond based on group_mode
// and whether it was mentioned.
func (b *Bot) shouldRespondInGroup(mentions []*larkim.MentionEvent) bool {
	switch b.cfg.GroupMode {
	case "disabled":
		return false
	case "always":
		return true
	default: // "mention"
		return isBotMentioned(mentions)
	}
}

// isBotMentioned checks if any mention targets the bot itself (key "mention_all" is @all).
func isBotMentioned(mentions []*larkim.MentionEvent) bool {
	for _, m := range mentions {
		if m.Key != nil && *m.Key != "@_all" {
			// Any non-@all mention key means the bot was mentioned
			// (Feishu only delivers events for messages that mention the bot in groups).
			return true
		}
	}
	return len(mentions) > 0
}

// stripMentions removes @mention placeholders from message text.
func stripMentions(text string, mentions []*larkim.MentionEvent) string {
	for _, m := range mentions {
		if m.Key != nil {
			text = strings.ReplaceAll(text, *m.Key, "")
		}
	}
	return strings.TrimSpace(text)
}
