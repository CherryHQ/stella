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
	"golang.org/x/oauth2"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const qqMaxMessageLen = 3500

func logger() *slog.Logger { return slog.With("component", "qq") }

// Config holds QQ Bot settings.
type Config struct {
	InstanceID string
	AppID      string
	AppSecret  string
}

// Bot wraps a QQ bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	api            openapi.OpenAPI
	creds          *token.QQBotCredentials
	tokenSource    oauth2.TokenSource
	sessionManager botgo.SessionManager
	handler        channel.Handler

	chatModels map[string]channel.ModelOption

	cfg          Config
	ctx          context.Context
	cancel       context.CancelFunc
	finalizeOnce sync.Once
}

// New creates a QQ bot. Call Start to begin receiving events.
func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("qq: app_id and app_secret are required")
	}

	b := &Bot{
		handler:    handler,
		chatModels: make(map[string]channel.ModelOption),
		cfg:        cfg,
	}
	if registrar, ok := handler.(interface {
		RegisterGroupPublisher(string, internalchannel.GroupPublisher)
	}); ok {
		registrar.RegisterGroupPublisher(b.Name(), b)
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

// Finalize removes routing registrations after accepted work has drained.
func (b *Bot) Finalize() {
	b.finalizeOnce.Do(func() {
		if registrar, ok := b.handler.(interface{ UnregisterGroupPublisher(string) }); ok {
			registrar.UnregisterGroupPublisher(b.Name())
		}
	})
}

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string {
	return qqChannelName(b.cfg.InstanceID)
}

func (b *Bot) Platform() string { return channel.PlatformQQ }

func qqChannelName(instanceID string) string {
	if instanceID != "" {
		return instanceID
	}
	return channel.PlatformQQ
}

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

// channelForC2C returns the channel identifier for a C2C (private) chat.
func channelForC2C(userID string) string {
	return "qq:c2c:" + userID
}

// channelForGroup returns the channel identifier for a group chat.
func channelForGroup(groupID string) string {
	return "qq:group:" + groupID
}

func incomingMsg(authorID, groupID string, content []ai.ContentBlock) channel.IncomingMessage {
	return incomingMsgForChannel(qqChannelName(""), authorID, groupID, content)
}

// incomingMsg builds an IncomingMessage from QQ message context.
func (b *Bot) incomingMsg(authorID, groupID string, content []ai.ContentBlock) channel.IncomingMessage {
	return incomingMsgForChannel(b.Name(), authorID, groupID, content)
}

func incomingMsgForChannel(channelID, authorID, groupID string, content []ai.ContentBlock) channel.IncomingMessage {
	chatID := channelForC2C(authorID)
	isGroup := groupID != ""
	if isGroup {
		chatID = channelForGroup(groupID)
	}
	return channel.IncomingMessage{
		Platform:   channel.PlatformQQ,
		ChannelID:  channelID,
		SenderID:   authorID,
		SenderName: "",
		ChatID:     chatID,
		IsGroup:    isGroup,
		Content:    content,
	}
}

// fillQQMeta populates the group-chat metadata fields (D3) on incoming from the
// raw QQ message. Fields are left zero when the platform omits the data.
func fillQQMeta(incoming *channel.IncomingMessage, msg *dto.Message) {
	incoming.MessageID = msg.ID
	incoming.Timestamp = qqEventTime(msg.Timestamp)
	if msg.MessageReference != nil {
		incoming.ReplyTo = msg.MessageReference.MessageID
	}
	incoming.Mentions = qqMentions(msg.Mentions)
}

// qqEventTime converts a QQ RFC3339 timestamp to UTC, or zero if unparseable.
func qqEventTime(ts dto.Timestamp) time.Time {
	t, err := ts.Time()
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// qqMentions normalizes QQ @-mentions. AgentID is resolved later by the dispatcher.
func qqMentions(users []*dto.User) []channel.Mention {
	var out []channel.Mention
	for _, u := range users {
		if u == nil {
			continue
		}
		out = append(out, channel.Mention{Raw: u.Username, PlatformID: u.ID})
	}
	return out
}
