package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"

	tele "gopkg.in/telebot.v4"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const telegramMaxMessageLen = 4000

// logger returns the package logger, always using the current default handler.
// This must be a function (not a package-level var) because the default handler
// is set in main() after package init.
func logger() *slog.Logger { return slog.With("component", "telegram") }

// Config holds Telegram bot settings.
type Config struct {
	InstanceID string // configured channel instance ID
	Token      string // bot token
	ChannelID  string // broadcast channel ID or @username
}

// Bot wraps a Telegram bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	bot     *tele.Bot
	handler channel.Handler
	md      goldmarkMD

	mu           sync.RWMutex
	chatModels   map[int64]channel.ModelOption
	finalizeOnce sync.Once

	cfg Config
	ctx context.Context
}

// New creates a Telegram bot and registers handlers. Call Start to begin polling.
func New(cfg Config, handler channel.Handler) (*Bot, error) {
	bot, err := tele.NewBot(tele.Settings{
		Token: cfg.Token,
		Poller: &tele.LongPoller{
			Timeout:        30 * time.Second,
			AllowedUpdates: tele.AllowedUpdates,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create bot: %w", err)
	}

	b := &Bot{
		bot:        bot,
		handler:    handler,
		md:         tgmd.TGMD(),
		chatModels: make(map[int64]channel.ModelOption),
		cfg:        cfg,
	}

	b.registerHandlers()

	if registrar, ok := handler.(channel.BotRegistrar); ok && bot.Me.Username != "" {
		registrar.RegisterBotIdentity(channel.PlatformTelegram, bot.Me.Username, cfg.InstanceID)
	}
	if registrar, ok := handler.(interface {
		RegisterGroupPublisher(string, internalchannel.GroupPublisher)
	}); ok {
		registrar.RegisterGroupPublisher(b.Name(), b)
	}

	return b, nil
}

// Start begins long polling. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx = ctx

	if err := registerCommands(b.bot); err != nil {
		logger().Warn("register telegram commands failed", "error", err)
	}

	logger().Info("polling started")

	go func() {
		<-ctx.Done()
		logger().Info("polling stopped")
		b.bot.Stop()
	}()

	b.bot.Start()
	return ctx.Err()
}

// Stop gracefully shuts down the Telegram bot. Implements channel.Channel.
func (b *Bot) Stop() {
	logger().Info("stopping telegram bot")
	b.bot.Stop()
}

// Finalize removes routing registrations after accepted work has drained.
func (b *Bot) Finalize() {
	b.finalizeOnce.Do(func() {
		if registrar, ok := b.handler.(interface {
			UnregisterBotIdentity(string, string, string)
		}); ok && b.bot.Me.Username != "" {
			registrar.UnregisterBotIdentity(channel.PlatformTelegram, b.bot.Me.Username, b.cfg.InstanceID)
		}
		if registrar, ok := b.handler.(interface{ UnregisterGroupPublisher(string) }); ok {
			registrar.UnregisterGroupPublisher(b.Name())
		}
	})
}

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformTelegram
}

func (b *Bot) Platform() string { return channel.PlatformTelegram }

// Notify sends a message to the specified chat. Implements channel.Channel.
func (b *Bot) Notify(_ context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		chatID = b.cfg.ChannelID
	}
	if chatID == "" {
		return fmt.Errorf("no target chat ID")
	}

	// Support both numeric IDs and @username for channels.
	var chat tele.Recipient
	if numID, err := strconv.ParseInt(chatID, 10, 64); err == nil {
		chat = &tele.Chat{ID: numID}
	} else {
		chat = chatRef(chatID)
	}

	logger().Debug("sending notification", "chat_id", chatID, "text_len", len(n.Text), "silent", n.Silent)

	opts := &tele.SendOptions{ParseMode: tele.ModeMarkdownV2}
	if n.Silent {
		opts.DisableNotification = true
	}

	if err := b.sendChunkedMarkdown(chat, n.Text, n.Silent, opts); err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	logger().Debug("notification sent successfully", "chat_id", chatID)
	return nil
}

// chatRef wraps a string (like "@channel_name") as a tele.Recipient.
type chatRef string

func (c chatRef) Recipient() string { return string(c) }

// guard wraps a handler, logging callback pass-through for diagnostics.
func (b *Bot) guard(h tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Callback() != nil {
			logger().Debug("guard: passing callback through", "data", c.Callback().Data, "unique", c.Callback().Unique)
		}
		return h(c)
	}
}

// isGroup returns true if the message is from a group or supergroup.
func isGroup(c tele.Context) bool {
	t := c.Chat().Type
	return t == tele.ChatGroup || t == tele.ChatSuperGroup
}

// stripBotMention removes @botname from the message text.
func (b *Bot) stripBotMention(text string) string {
	if b.bot.Me.Username == "" {
		return text
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "@"+b.bot.Me.Username, ""))
}

// incomingMsg builds an IncomingMessage from the Telegram context.
func (b *Bot) incomingMsg(c tele.Context, content []ai.ContentBlock) channel.IncomingMessage {
	sender := c.Sender()
	senderID := ""
	senderName := ""
	if sender != nil {
		senderID = fmt.Sprintf("%d", sender.ID)
		senderName = sender.FirstName
		if senderName == "" {
			senderName = sender.Username
		}
	}
	im := channel.IncomingMessage{
		Platform:   channel.PlatformTelegram,
		ChannelID:  b.Name(),
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     fmt.Sprintf("%d", c.Chat().ID),
		IsGroup:    isGroup(c),
		Content:    content,
	}
	if m := c.Message(); m != nil {
		im.MessageID = fmt.Sprintf("%d", m.ID)
		im.Timestamp = m.Time()
		if m.ThreadID != 0 {
			im.ThreadID = strconv.Itoa(m.ThreadID)
		}
		if m.ReplyTo != nil {
			im.ReplyTo = fmt.Sprintf("%d", m.ReplyTo.ID)
		}
		im.Mentions = telegramMentions(m)
	}
	return im
}

// telegramMentions extracts normalized @-mentions from a message's entities.
// AgentID is left empty — the dispatcher resolves it from group membership.
func telegramMentions(m *tele.Message) []channel.Mention {
	var mentions []channel.Mention
	for _, e := range m.Entities {
		switch e.Type {
		case tele.EntityMention: // @username
			raw := m.EntityText(e)
			mentions = append(mentions, channel.Mention{
				Raw:        raw,
				PlatformID: strings.TrimPrefix(raw, "@"),
			})
		case tele.EntityTMention: // text_mention carrying a User
			if e.User != nil {
				mentions = append(mentions, channel.Mention{
					Raw:        m.EntityText(e),
					PlatformID: fmt.Sprintf("%d", e.User.ID),
				})
			}
		}
	}
	return mentions
}
