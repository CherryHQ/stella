package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

const maxMessageLength = 2000

type Config struct {
	InstanceID      string
	Token           string
	AllowedGuildIDs string
}

type Bot struct {
	session           *discordgo.Session
	handler           channel.Handler
	cfg               Config
	allowedGuilds     map[string]struct{}
	ctx               context.Context
	mu                sync.RWMutex
	provisionMu       sync.Mutex
	provisionedGroups map[string]struct{}
	closeOnce         sync.Once
	finalized         bool
	botID             string
}

func New(cfg Config, handler channel.Handler) (*Bot, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("discord: bot token is required")
	}
	s, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	allowedGuilds := make(map[string]struct{})
	for _, guildID := range strings.FieldsFunc(cfg.AllowedGuildIDs, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		allowedGuilds[guildID] = struct{}{}
	}
	return &Bot{session: s, handler: handler, cfg: cfg, allowedGuilds: allowedGuilds, provisionedGroups: make(map[string]struct{})}, nil
}

func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformDiscord
}
func (b *Bot) Platform() string { return channel.PlatformDiscord }

func (b *Bot) guildAllowed(guildID string) bool {
	if guildID == "" {
		return true
	}
	_, ok := b.allowedGuilds[guildID]
	return ok
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
	if len(b.allowedGuilds) == 0 {
		logger().Info("discord guild messages disabled; configure allowed_guild_ids to enable trusted servers")
	} else {
		guildIDs := make([]string, 0, len(b.allowedGuilds))
		for guildID := range b.allowedGuilds {
			guildIDs = append(guildIDs, guildID)
		}
		sort.Strings(guildIDs)
		logger().Info("discord guild allowlist configured", "guild_ids", guildIDs)
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
	if message != nil && message.GuildID != "" {
		return "Error: Stella could not process this server message."
	}
	return "Error: " + err.Error()
}

func (b *Bot) incomingMessage(m *discordgo.Message, content []channelContentBlock) channel.IncomingMessage {
	blocks := unwrapContent(content)
	name := m.Author.GlobalName
	if name == "" {
		name = m.Author.Username
	}
	im := channel.IncomingMessage{Platform: channel.PlatformDiscord, ChannelID: b.Name(), SenderID: m.Author.ID, SenderName: name, ChatID: m.ChannelID, IsGroup: m.GuildID != "", MessageID: m.ID, Timestamp: m.Timestamp.UTC(), Content: blocks}
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
