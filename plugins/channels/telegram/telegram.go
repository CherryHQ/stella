package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"
	"golang.org/x/sync/singleflight"

	tele "gopkg.in/telebot.v4"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const telegramMaxMessageLen = 4000

const (
	groupProvisionTimeout       = 10 * time.Second
	groupProvisionFailureTTL    = 30 * time.Second
	maxProvisionTrackingEntries = 1024
)

type groupMemberProvisioner interface {
	EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
}

type threadGroupMemberProvisioner interface {
	EnsurePlatformThreadGroupMember(ctx context.Context, platform, platformGroupID, platformThreadID, legacyPlatformGroupID, channelID string) error
}

// logger returns the package logger, always using the current default handler.
// This must be a function (not a package-level var) because the default handler
// is set in main() after package init.
func logger() *slog.Logger { return slog.With("component", "telegram") }

// Config holds Telegram bot settings.
type Config struct {
	InstanceID      string
	Token           string
	ChannelID       string
	AllowGroup      bool
	AllowedChatIDs  []string
	AllowedTopicIDs []string
	AllowDM         bool
	RequireMention  bool
}

// Bot wraps a Telegram bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	bot     *tele.Bot
	handler channel.Handler
	md      goldmarkMD

	finalizeOnce      sync.Once
	provisionMu       sync.RWMutex
	provisionGroup    singleflight.Group
	provisionedGroups map[string]struct{}
	provisionFailures map[string]time.Time
	provisionWarnings map[string]struct{}

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
		bot:               bot,
		handler:           handler,
		md:                tgmd.TGMD(),
		provisionedGroups: make(map[string]struct{}),
		provisionWarnings: make(map[string]struct{}),
		cfg:               cfg,
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
func (b *Bot) guard(directed bool, h tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Callback() != nil {
			logger().Debug("guard: passing callback through", "data", c.Callback().Data, "unique", c.Callback().Unique)
		}
		if !b.admit(c, directed) {
			return nil
		}
		return h(c)
	}
}

func (b *Bot) admit(c tele.Context, directed bool) bool {
	if !isGroup(c) {
		return b.cfg.AllowDM
	}
	chatID := strconv.FormatInt(c.Chat().ID, 10)
	if !b.cfg.AllowGroup {
		b.warnGroupRejectionOnce(chatID, "groups_disabled", nil)
		return false
	}
	topicID := telegramTopicID(c.Message())
	if !b.groupAllowed(chatID, topicID) {
		b.warnGroupRejectionOnce(chatID, "not_allowlisted", nil)
		return false
	}
	if b.cfg.RequireMention && !directed && !b.botMentioned(c.Message()) && !b.replyToBot(c.Message()) && !b.commandAddressesBot(c.Message()) {
		return false
	}
	return b.ensureGroupMember(chatID, topicID)
}

// groupAllowed is fail-closed when either optional list is configured. Topic
// entries bind a topic to its chat because Telegram thread IDs are not global.
func (b *Bot) groupAllowed(chatID, topicID string) bool {
	if len(b.cfg.AllowedChatIDs) > 0 && !containsID(b.cfg.AllowedChatIDs, chatID) {
		return false
	}
	if len(b.cfg.AllowedTopicIDs) == 0 {
		return true
	}
	return topicID != "" && containsID(b.cfg.AllowedTopicIDs, chatID+":"+topicID)
}

func containsID(ids []string, want string) bool {
	return slices.Contains(ids, want)
}

func (b *Bot) ensureGroupMember(chatID, topicID string) bool {
	cacheKey := chatID + "\x00" + topicID
	if b.ctx == nil {
		b.warnGroupRejectionOnce(cacheKey, "bot_lifecycle_unavailable", nil)
		return false
	}
	if admitted, retry := b.groupProvisionState(cacheKey, time.Now()); admitted || !retry {
		return admitted
	}
	result, _, _ := b.provisionGroup.Do(cacheKey, func() (any, error) {
		if admitted, retry := b.groupProvisionState(cacheKey, time.Now()); admitted || !retry {
			return admitted, nil
		}
		ctx, cancel := context.WithTimeout(b.ctx, groupProvisionTimeout)
		defer cancel()
		var err error
		if topicID != "" {
			provisioner, ok := b.handler.(threadGroupMemberProvisioner)
			if !ok {
				b.warnGroupRejectionOnce(cacheKey, "thread_provisioner_unavailable", nil)
				return false, nil
			}
			// Telegram topics were always children of this chat; unlike Discord
			// threads, there is no historical topic-as-parent identity to adopt.
			// Passing chatID here would rename the parent group's state into the
			// first topic that receives a message.
			err = provisioner.EnsurePlatformThreadGroupMember(ctx, channel.PlatformTelegram, chatID, topicID, "", b.Name())
		} else {
			provisioner, ok := b.handler.(groupMemberProvisioner)
			if !ok {
				b.warnGroupRejectionOnce(cacheKey, "provisioner_unavailable", nil)
				return false, nil
			}
			err = provisioner.EnsurePlatformGroupMember(ctx, channel.PlatformTelegram, chatID, b.Name())
		}
		if err != nil {
			b.recordGroupProvisionFailure(cacheKey, time.Now().Add(groupProvisionFailureTTL))
			b.warnGroupRejectionOnce(cacheKey, "provision_failed", err)
			return false, nil
		}
		b.provisionMu.Lock()
		if b.provisionedGroups == nil {
			b.provisionedGroups = make(map[string]struct{})
		}
		b.provisionedGroups[cacheKey] = struct{}{}
		delete(b.provisionFailures, cacheKey)
		b.provisionMu.Unlock()
		return true, nil
	})
	admitted, _ := result.(bool)
	return admitted
}

func (b *Bot) groupProvisionState(chatID string, now time.Time) (admitted, retry bool) {
	b.provisionMu.RLock()
	defer b.provisionMu.RUnlock()
	if _, ok := b.provisionedGroups[chatID]; ok {
		return true, false
	}
	return false, !b.provisionFailures[chatID].After(now)
}

func (b *Bot) recordGroupProvisionFailure(chatID string, retryAt time.Time) {
	b.provisionMu.Lock()
	defer b.provisionMu.Unlock()
	if b.provisionFailures == nil {
		b.provisionFailures = make(map[string]time.Time)
	}
	for id, expiry := range b.provisionFailures {
		if !expiry.After(time.Now()) {
			delete(b.provisionFailures, id)
		}
	}
	if len(b.provisionFailures) < maxProvisionTrackingEntries {
		b.provisionFailures[chatID] = retryAt
	}
}

func (b *Bot) warnGroupRejectionOnce(chatID, reason string, err error) {
	b.provisionMu.Lock()
	defer b.provisionMu.Unlock()
	b.warnGroupRejectionOnceLocked(chatID, reason, err)
}

func (b *Bot) warnGroupRejectionOnceLocked(chatID, reason string, err error) {
	if b.provisionWarnings == nil {
		b.provisionWarnings = make(map[string]struct{})
	}
	key := chatID + "\x00" + reason
	if _, logged := b.provisionWarnings[key]; logged {
		return
	}
	if len(b.provisionWarnings) >= maxProvisionTrackingEntries {
		return
	}
	b.provisionWarnings[key] = struct{}{}
	args := []any{"chat_id", chatID, "reason", reason}
	if err != nil {
		args = append(args, "error", err)
	}
	logger().Warn("telegram group message rejected", args...)
}

func (b *Bot) botMentioned(m *tele.Message) bool {
	if m == nil || b.bot.Me.Username == "" {
		return false
	}
	for _, mention := range telegramMentions(m) {
		if strings.EqualFold(mention.PlatformID, b.bot.Me.Username) {
			return true
		}
	}
	return false
}

func (b *Bot) replyToBot(m *tele.Message) bool {
	return m != nil && m.ReplyTo != nil && m.ReplyTo.Sender != nil && b.bot.Me != nil && m.ReplyTo.Sender.ID == b.bot.Me.ID
}

// commandAddressesBot recognizes Telegram's native /command@botname form.
// Bare commands do not bypass group mention policy.
func (b *Bot) commandAddressesBot(m *tele.Message) bool {
	if m == nil || b.bot.Me == nil || b.bot.Me.Username == "" {
		return false
	}
	command := strings.Fields(m.Text)
	if len(command) == 0 || !strings.HasPrefix(command[0], "/") {
		return false
	}
	_, target, found := strings.Cut(command[0], "@")
	return found && strings.EqualFold(target, b.bot.Me.Username)
}

// telegramTopicID only treats forum-topic identity as a group sub-thread. A
// normal reply has ReplyTo but no TopicMessage and must remain in the parent
// group session.
func telegramTopicID(m *tele.Message) string {
	if m == nil || !m.TopicMessage || m.ThreadID == 0 {
		return ""
	}
	return strconv.Itoa(m.ThreadID)
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
		im.ThreadID = telegramTopicID(m)
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
	entities := m.Entities
	if len(m.CaptionEntities) > 0 {
		entities = m.CaptionEntities
	}
	for _, e := range entities {
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
