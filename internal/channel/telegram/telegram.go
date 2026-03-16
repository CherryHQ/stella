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

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	tele "gopkg.in/telebot.v4"
)

const telegramMaxMessageLen = 4000

// logger returns the package logger, always using the current default handler.
// This must be a function (not a package-level var) because the default handler
// is set in main() after package init.
func logger() *slog.Logger { return slog.With("component", "telegram") }

// Config holds Telegram bot settings.
type Config struct {
	Token      string  // bot token
	NotifyChat string  // default chat ID for proactive notifications
	ChannelID  string  // broadcast channel ID or @username
	GroupMode  string  // "mention" | "always" | "disabled"
	AllowedIDs []int64 // user IDs allowed to use the bot (empty = allow all)
}

// Bot wraps a Telegram bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	bot         *tele.Bot
	pool        *agent.Pool // fallback pool (default agent)
	poolManager *agent.PoolManager
	store       config.Store
	agentCmd    *channel.AgentCommander
	cmd         *channel.Commander
	listFn      channel.ModelListFunc
	switchFn    channel.ModelSwitchFunc
	md          goldmarkMD

	mu         sync.RWMutex
	chatModels map[int64]channel.ModelOption

	allowed map[int64]struct{} // empty map = allow all
	cfg     Config
	ctx     context.Context
}

// New creates a Telegram bot and registers handlers. Call Start to begin polling.
// poolManager and store are used for multi-user/multi-agent routing. When set,
// each message resolves user -> agent -> pool dynamically. The pool parameter
// is used as a fallback for the Commander (session rotation, compaction).
func New(cfg Config, pool *agent.Pool, listFn channel.ModelListFunc, switchFn channel.ModelSwitchFunc, opts ...BotOption) (*Bot, error) {
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

	if cfg.GroupMode == "" {
		cfg.GroupMode = "mention"
	}

	allowed := make(map[int64]struct{}, len(cfg.AllowedIDs))
	for _, id := range cfg.AllowedIDs {
		allowed[id] = struct{}{}
	}

	poolAdapter := &channel.PoolAdapter[agent.SessionInfo]{
		ResolveFunc: pool.ResolveSession,
		RotateFunc:  pool.RotateSession,
		CompactFunc: pool.CompactSession,
		AdaptFn:     func(info agent.SessionInfo) channel.SessionInfo { return channel.SessionInfo{ID: info.ID} },
	}

	b := &Bot{
		bot:        bot,
		pool:       pool,
		cmd:        channel.NewCommander(poolAdapter, listFn, switchFn),
		listFn:     listFn,
		switchFn:   switchFn,
		md:         tgmd.TGMD(),
		chatModels: make(map[int64]channel.ModelOption),
		allowed:    allowed,
		cfg:        cfg,
	}

	for _, opt := range opts {
		opt(b)
	}

	b.registerHandlers()
	return b, nil
}

// BotOption configures the Telegram Bot.
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

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string { return "telegram" }

// Notify sends a message to the specified chat. Implements channel.Channel.
func (b *Bot) Notify(_ context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		chatID = b.cfg.NotifyChat
	}
	if chatID == "" {
		chatID = b.cfg.ChannelID
	}
	if chatID == "" {
		return fmt.Errorf("no target chat ID")
	}

	// Support both numeric IDs and @username for channels.
	var chat tele.Recipient
	if id, err := strconv.ParseInt(chatID, 10, 64); err == nil {
		chat = &tele.Chat{ID: id}
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

// guard wraps a handler with access control and group mode checks.
func (b *Bot) guard(h tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if !b.isAllowed(c) {
			if s := c.Sender(); s != nil {
				logger().Warn("unauthorized access", "user_id", s.ID)
			}
			return nil
		}
		// Skip group filtering for callback queries — they originate from
		// the bot's own inline keyboards (e.g. model selection) and don't
		// carry mention/reply context.
		if isGroup(c) && c.Callback() == nil && !b.shouldRespondInGroup(c) {
			logger().Debug("guard: skipped group message", "chat", c.Chat().ID)
			return nil
		}
		if c.Callback() != nil {
			logger().Debug("guard: passing callback through", "data", c.Callback().Data, "unique", c.Callback().Unique)
		}
		return h(c)
	}
}

// isAllowed returns true if the sender is in the allowed list.
// An empty allowed list means everyone is allowed.
func (b *Bot) isAllowed(c tele.Context) bool {
	if len(b.allowed) == 0 {
		return true
	}
	if c.Sender() == nil {
		return false
	}
	_, ok := b.allowed[c.Sender().ID]
	return ok
}

// isGroup returns true if the message is from a group or supergroup.
func isGroup(c tele.Context) bool {
	t := c.Chat().Type
	return t == tele.ChatGroup || t == tele.ChatSuperGroup
}

// shouldRespondInGroup checks whether the bot should respond based on group_mode.
func (b *Bot) shouldRespondInGroup(c tele.Context) bool {
	switch b.cfg.GroupMode {
	case "disabled":
		return false
	case "always":
		return true
	default: // "mention"
		return b.isMentionedOrReplied(c)
	}
}

// isMentionedOrReplied returns true if the bot is @mentioned in the text
// or the message is a reply to one of the bot's messages.
func (b *Bot) isMentionedOrReplied(c tele.Context) bool {
	// Check for reply to bot.
	if reply := c.Message().ReplyTo; reply != nil && reply.Sender != nil {
		if reply.Sender.ID == b.bot.Me.ID {
			return true
		}
	}
	// Check for @mention in text or caption (photos carry text in Caption).
	if b.bot.Me.Username != "" {
		mention := "@" + b.bot.Me.Username
		if strings.Contains(c.Message().Text, mention) || strings.Contains(c.Message().Caption, mention) {
			return true
		}
	}
	return false
}

// stripBotMention removes @botname from the message text.
func (b *Bot) stripBotMention(text string) string {
	if b.bot.Me.Username == "" {
		return text
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "@"+b.bot.Me.Username, ""))
}

// channelForChat returns the channel identifier for a Telegram chat.
// Each chat (private or group) gets its own channel namespace.
// Used as a fallback when multi-agent routing is not configured.
func channelForChat(c tele.Context) string {
	return "tg" + strconv.FormatInt(c.Chat().ID, 10)
}

// resolveSession returns the active session ID for the current chat,
// creating a new session if none exists.
func (b *Bot) resolveSession(c tele.Context) (string, error) {
	pool := b.resolvePool(c)
	sessionKey := b.buildSessionKey(c, pool.AgentID())
	info, err := pool.ResolveSession(sessionKey)
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// buildSessionKey constructs a session key for the given Telegram context.
// When multi-agent routing is configured, uses BuildSessionKey for proper
// per-agent scoping; otherwise falls back to the legacy format.
func (b *Bot) buildSessionKey(c tele.Context, agentID string) string {
	if b.poolManager == nil || b.store == nil || agentID == "" {
		return channelForChat(c)
	}
	externalUserID := strconv.FormatInt(c.Sender().ID, 10)
	channelCtx := "private"
	if isGroup(c) {
		channelCtx = "group:" + strconv.FormatInt(c.Chat().ID, 10)
	}
	return agent.BuildSessionKey(agentID, "tg", externalUserID, channelCtx)
}

// resolvePool resolves the pool for the current chat context.
// If poolManager and store are configured, it does: resolve user -> resolve agent -> get pool.
// Otherwise falls back to the default pool.
func (b *Bot) resolvePool(c tele.Context) *agent.Pool {
	if b.poolManager == nil || b.store == nil {
		return b.pool
	}

	sender := c.Sender()
	if sender == nil {
		return b.pool
	}

	ctx := context.Background()
	externalID := strconv.FormatInt(sender.ID, 10)
	name := sender.FirstName
	if name == "" {
		name = sender.Username
	}

	user, err := channel.ResolveUser(ctx, b.store, externalID, "telegram", name)
	if err != nil {
		logger().Warn("resolve user failed, using default pool", "error", err)
		return b.pool
	}

	chatCtx := channel.ChatContext{
		Platform: "telegram",
		ChatID:   strconv.FormatInt(c.Chat().ID, 10),
		IsGroup:  isGroup(c),
	}

	agentID, err := channel.ResolveAgent(ctx, b.store, user, chatCtx)
	if err != nil {
		logger().Warn("resolve agent failed, using default pool", "error", err)
		return b.pool
	}

	pool := b.poolManager.Get(agentID)
	if pool == nil {
		logger().Warn("agent pool not found, using default pool", "agent_id", agentID)
		return b.pool
	}

	return pool
}
