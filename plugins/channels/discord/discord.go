package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	maxMessageLength           = 2000
	typingInterval             = 8 * time.Second
	streamEditInterval         = 1500 * time.Millisecond
	threadContextMaxLen        = 24 << 10
	provisionedGroupCacheLimit = 1024
	// Bound lazy history to the starter plus the latest 20 messages; raise this
	// only when thread-context truncation is observed in real deployments.
	threadHistoryLimit = 20
)

type discordREST interface {
	Channel(channelID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelMessage(channelID, messageID string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessages(channelID string, limit int, beforeID, afterID, aroundID string, options ...discordgo.RequestOption) ([]*discordgo.Message, error)
	ChannelTyping(channelID string, options ...discordgo.RequestOption) error
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(message *discordgo.MessageEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageDelete(channelID, messageID string, options ...discordgo.RequestOption) error
}

type Config struct {
	InstanceID        string
	Token             string
	AllowGroup        bool
	AllowAllGuilds    bool
	AllowedGuildIDs   []string
	AllowedChannelIDs []string
	AllowedUserIDs    []string
	AllowedRoleIDs    []string
	AllowDM           bool
	RequireMention    bool
}

type Bot struct {
	session           *discordgo.Session
	handler           channel.Handler
	cfg               Config
	ctx               context.Context
	mu                sync.RWMutex
	provisionMu       sync.Mutex
	provisionedGroups map[string]struct{}
	typingMu          sync.Mutex
	typing            map[string]*typingState
	closeOnce         sync.Once
	finalized         bool
	botID             string
	rest              discordREST
}

func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("discord: bot token is required")
	}
	s, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	return &Bot{session: s, handler: handler, cfg: cfg, provisionedGroups: make(map[string]struct{}), typing: make(map[string]*typingState), rest: s}, nil
}

func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformDiscord
}
func (b *Bot) Platform() string { return channel.PlatformDiscord }

// groupAccessAllowed reports whether a guild message may be served and
// resolves its thread-aware route. It is fail-closed: once `allow_group` is
// on, an operator must either flip the explicit, dangerous `allow_all_guilds`
// switch or list at least one guild, channel, user, or role — an `allow_group`
// with every allowlist empty and `allow_all_guilds` off denies everything
// rather than silently reopening every joined server. Matching the parent of a
// thread may require a read-only Channel lookup, so the resolved route is
// always returned for the caller to reuse.
func (b *Bot) groupAccessAllowed(ctx context.Context, m *discordgo.Message) (bool, messageRoute, error) {
	if !b.cfg.AllowGroup {
		return false, messageRoute{}, nil
	}
	if !b.cfg.AllowAllGuilds && len(b.cfg.AllowedGuildIDs) == 0 && len(b.cfg.AllowedChannelIDs) == 0 &&
		len(b.cfg.AllowedUserIDs) == 0 && len(b.cfg.AllowedRoleIDs) == 0 {
		return false, messageRoute{}, nil
	}
	if b.cfg.AllowAllGuilds ||
		containsID(b.cfg.AllowedGuildIDs, m.GuildID) ||
		(m.Author != nil && containsID(b.cfg.AllowedUserIDs, m.Author.ID)) ||
		memberHasAllowedRole(m.Member, b.cfg.AllowedRoleIDs) ||
		containsID(b.cfg.AllowedChannelIDs, m.ChannelID) {
		route, err := b.resolveMessageRoute(ctx, m)
		return true, route, err
	}
	if len(b.cfg.AllowedChannelIDs) == 0 {
		return false, messageRoute{}, nil
	}
	// Only a channel-allowlist match can still admit this message, and
	// evaluating it against a thread's parent needs a Channel lookup. Failing
	// that lookup cannot confirm access, so it denies rather than propagating
	// the error to a channel that was never authorized to see it.
	route, err := b.resolveMessageRoute(ctx, m)
	if err != nil {
		logger().Warn("resolve discord parent channel for allowlist check failed", "channel_id", m.ChannelID, "error", err)
		return false, messageRoute{}, nil
	}
	if route.chatID != "" && route.chatID != m.ChannelID && containsID(b.cfg.AllowedChannelIDs, route.chatID) {
		return true, route, nil
	}
	return false, messageRoute{}, nil
}

func containsID(ids []string, target string) bool {
	if target == "" {
		return false
	}
	return slices.Contains(ids, target)
}

func memberHasAllowedRole(member *discordgo.Member, allowedRoleIDs []string) bool {
	if member == nil {
		return false
	}
	for _, roleID := range member.Roles {
		if containsID(allowedRoleIDs, roleID) {
			return true
		}
	}
	return false
}

func (b *Bot) mentioned(m *discordgo.Message) bool {
	if b.session.State == nil || b.session.State.User == nil {
		return false
	}
	botID := b.session.State.User.ID
	for _, user := range m.Mentions {
		if user != nil && user.ID == botID {
			return true
		}
	}
	return false
}

func (b *Bot) addressed(m *discordgo.Message) bool {
	if b.mentioned(m) {
		return true
	}
	return b.session.State != nil && b.session.State.User != nil && m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == b.session.State.User.ID
}

func (b *Bot) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.session.AddHandler(b.onMessageCreate)
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open discord gateway: %w", err)
	}
	if err := b.activate(ctx); err != nil {
		b.Stop()
		return err
	}
	<-ctx.Done()
	b.Stop()
	return ctx.Err()
}

func (b *Bot) activate(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return fmt.Errorf("discord: finalized during startup")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.session.State != nil && b.session.State.User != nil {
		b.botID = b.session.State.User.ID
		if r, ok := b.handler.(channel.BotRegistrar); ok {
			r.RegisterBotIdentity(channel.PlatformDiscord, b.botID, b.Name())
		}
	}
	if r, ok := b.handler.(interface {
		RegisterGroupPublisher(string, internalchannel.GroupPublisher)
	}); ok {
		r.RegisterGroupPublisher(b.Name(), b)
	}
	if !b.cfg.AllowGroup {
		logger().Info("discord server-channel messages disabled; enable allow_group to serve the servers this bot joined")
	}
	// Publishing the context is the ingress activation point. Keep it last so
	// event handlers cannot accept traffic before all routing state is ready.
	b.ctx = ctx
	return nil
}

// Finalize removes routing registrations after accepted work has drained.
func (b *Bot) Finalize() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	b.finalized = true
	b.ctx = nil
	b.stopTypingHeartbeats()
	if b.botID != "" {
		if r, ok := b.handler.(interface {
			UnregisterBotIdentity(string, string, string)
		}); ok {
			r.UnregisterBotIdentity(channel.PlatformDiscord, b.botID, b.Name())
		}
	}
	if r, ok := b.handler.(interface{ UnregisterGroupPublisher(string) }); ok {
		r.UnregisterGroupPublisher(b.Name())
	}
}

func (b *Bot) Stop() {
	b.stopTypingHeartbeats()
	b.closeOnce.Do(func() {
		if err := b.session.Close(); err != nil {
			slog.Default().Warn("close discord session failed", "error", err)
		}
	})
}

func (b *Bot) Notify(ctx context.Context, n channel.Notification) error {
	if n.RecipientID != "" {
		dm, err := b.session.UserChannelCreate(n.RecipientID)
		if err != nil {
			return fmt.Errorf("discord: create recipient DM: %w", err)
		}
		if dm == nil || dm.ID == "" {
			return fmt.Errorf("discord: recipient DM has no channel ID")
		}
		n.ChatID = dm.ID
	}
	if n.ChatID == "" {
		return fmt.Errorf("discord: no target chat ID")
	}
	return b.sendTextOptions(ctx, n.ChatID, n.Text, "", n.Silent)
}

func logger() *slog.Logger { return slog.With("component", "discord") }

func (b *Bot) onMessageCreate(_ *discordgo.Session, event *discordgo.MessageCreate) {
	if event == nil || event.Message == nil || event.Author == nil || event.Author.Bot {
		return
	}
	b.mu.RLock()
	ctx := b.ctx
	b.mu.RUnlock()
	if ctx == nil {
		return
	}
	if err := b.handleMessage(ctx, event.Message); err != nil {
		logger().Warn("handle message failed", "error", err, "channel_id", event.ChannelID)
		_ = b.sendText(context.WithoutCancel(ctx), event.ChannelID, userFacingError(event.Message, err), event.ID)
	}
}

func userFacingError(message *discordgo.Message, err error) string {
	if errors.Is(err, errGuestAttachmentsUnsupported) {
		return "Attachments are not supported in guest chat. Send a text message instead."
	}
	if message != nil && message.GuildID != "" {
		return "Error: Stella could not process this server message."
	}
	return "Error: " + err.Error()
}

func (b *Bot) incomingMessage(m *discordgo.Message, content []channelContentBlock, chatID, threadID string) channel.IncomingMessage {
	blocks := unwrapContent(content)
	name := m.Author.GlobalName
	if name == "" {
		name = m.Author.Username
	}
	if chatID == "" {
		chatID = m.ChannelID
	}
	im := channel.IncomingMessage{Platform: channel.PlatformDiscord, ChannelID: b.Name(), SenderID: m.Author.ID, SenderName: name, ChatID: chatID, IsGroup: m.GuildID != "", ThreadID: threadID, MessageID: m.ID, Timestamp: m.Timestamp.UTC(), Content: blocks}
	if m.MessageReference != nil {
		im.ReplyTo = m.MessageReference.MessageID
	}
	for _, u := range m.Mentions {
		im.Mentions = append(im.Mentions, channel.Mention{Raw: "<@" + u.ID + ">", PlatformID: u.ID})
	}
	return im
}

func (b *Bot) stripBotMention(text string) string {
	if b.session.State == nil || b.session.State.User == nil {
		return text
	}
	id := b.session.State.User.ID
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "<@"+id+">", ""), "<@!"+id+">", ""))
}
