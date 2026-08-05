package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
)

const maxMessageLength = 2000

type Config struct{ InstanceID, Token string }

type Bot struct {
	session   *discordgo.Session
	handler   channel.Handler
	cfg       Config
	ctx       context.Context
	mu        sync.RWMutex
	closeOnce sync.Once
	finalized bool
	botID     string
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
	return &Bot{session: s, handler: handler, cfg: cfg}, nil
}

func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformDiscord
}
func (b *Bot) Platform() string { return channel.PlatformDiscord }

func (b *Bot) Start(ctx context.Context) error {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
	b.session.AddHandler(b.onMessageCreate)
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open discord gateway: %w", err)
	}
	b.mu.Lock()
	if b.finalized {
		b.mu.Unlock()
		b.Stop()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("discord: finalized during startup")
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
	b.mu.Unlock()
	<-ctx.Done()
	b.Stop()
	return ctx.Err()
}

// Finalize removes routing registrations after accepted work has drained.
func (b *Bot) Finalize() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	b.finalized = true
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
		_ = b.sendText(context.WithoutCancel(ctx), event.ChannelID, "Error: "+err.Error(), event.ID)
	}
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
