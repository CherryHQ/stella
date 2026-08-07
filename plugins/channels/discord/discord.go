package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

const discordMaxMessageLen = 2000

// logger returns the package logger, always using the current default handler.
// This must be a function (not a package-level var) because the default handler
// is set in main() after package init.
func logger() *slog.Logger { return slog.With("component", "discord") }

// Config holds Discord bot settings.
type Config struct {
	InstanceID      string   // configured channel instance ID
	Token           string   // bot token
	GuildID         string   // optional: restrict ingress to one server
	ChannelID       string   // optional default notify channel ID
	MentionOnly     bool     // only respond to @-mentions / whitelisted channels in guilds
	RespondChannels []string // channel-ID whitelist that bypasses MentionOnly
}

// Bot wraps a Discord bot with agent pool integration.
// It implements channel.Channel.
type Bot struct {
	session *discordgo.Session
	handler channel.Handler

	mu         sync.RWMutex
	chatModels map[string]channel.ModelOption
	closeOnce  sync.Once

	cfg Config
	ctx context.Context
}

// splitChannelList parses a comma-separated channel-ID list into a slice,
// trimming whitespace and dropping empties.
func splitChannelList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// New creates a Discord bot and registers handlers. Call Start to open the gateway.
func New(cfg Config, handler channel.Handler) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// MESSAGE_CONTENT is a privileged intent; it must also be enabled in the
	// Discord Developer Portal or guild message content arrives empty.
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentMessageContent

	b := &Bot{
		session:    session,
		handler:    handler,
		chatModels: make(map[string]channel.ModelOption),
		cfg:        cfg,
	}

	session.AddHandler(b.onMessageCreate)

	if registrar, ok := handler.(interface {
		RegisterGroupPublisher(string, internalchannel.GroupPublisher)
	}); ok {
		registrar.RegisterGroupPublisher(b.Name(), b)
	}

	return b, nil
}

// Start opens the Discord gateway. It blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx = ctx

	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open gateway: %w", err)
	}
	logger().Info("gateway opened")

	// Register bot identity by ID (Discord mentions are <@snowflake>) so the
	// group dispatcher can resolve @mentions to Stella agents.
	if registrar, ok := b.handler.(channel.BotRegistrar); ok && b.session.State != nil && b.session.State.User != nil {
		registrar.RegisterBotIdentity(channel.PlatformDiscord, b.session.State.User.ID, b.cfg.InstanceID)
	}

	<-ctx.Done()
	logger().Info("gateway closing")
	b.close()
	return ctx.Err()
}

// Stop gracefully shuts down the Discord bot. Implements channel.Channel.
func (b *Bot) Stop() {
	logger().Info("stopping discord bot")
	b.close()
}

// close closes the session at most once (Close is not safe to call twice).
func (b *Bot) close() {
	b.closeOnce.Do(func() {
		if err := b.session.Close(); err != nil {
			logger().Warn("close session failed", "error", err)
		}
	})
}

// Name returns the channel name. Implements channel.Channel.
func (b *Bot) Name() string {
	if b.cfg.InstanceID != "" {
		return b.cfg.InstanceID
	}
	return channel.PlatformDiscord
}

// Platform returns the platform identifier. Consumed by the notify dispatcher
// to route by-type notifications.
func (b *Bot) Platform() string { return channel.PlatformDiscord }

// Notify sends a message to the specified channel. Implements channel.Channel.
func (b *Bot) Notify(_ context.Context, n channel.Notification) error {
	chatID := n.ChatID
	if chatID == "" {
		chatID = b.cfg.ChannelID
	}
	if chatID == "" {
		return fmt.Errorf("no target channel ID")
	}

	logger().Debug("sending notification", "channel_id", chatID, "text_len", len(n.Text))

	if err := b.sendChunked(chatID, n.Text); err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	logger().Debug("notification sent successfully", "channel_id", chatID)
	return nil
}

// botUserID returns the bot's own user ID, or "" before the gateway is ready.
func (b *Bot) botUserID() string {
	if b.session.State != nil && b.session.State.User != nil {
		return b.session.State.User.ID
	}
	return ""
}

// stripBotMention removes <@id> and <@!id> mentions of the bot from the text.
func (b *Bot) stripBotMention(text string) string {
	id := b.botUserID()
	if id == "" {
		return strings.TrimSpace(text)
	}
	text = strings.ReplaceAll(text, "<@!"+id+">", "")
	text = strings.ReplaceAll(text, "<@"+id+">", "")
	return strings.TrimSpace(text)
}

// incomingMsg builds an IncomingMessage from a Discord message.
//
// IsGroup is always false (MVP decision A): every Discord message routes through
// the direct-answer path. The adapter-level trigger gate in onMessageCreate is
// what prevents flooding a server, not group dispatch.
func (b *Bot) incomingMsg(m *discordgo.MessageCreate, content []ai.ContentBlock) channel.IncomingMessage {
	senderID := ""
	senderName := ""
	if m.Author != nil {
		senderID = m.Author.ID
		senderName = m.Author.Username
		if m.Author.GlobalName != "" {
			senderName = m.Author.GlobalName
		}
	}
	im := channel.IncomingMessage{
		Platform:   channel.PlatformDiscord,
		ChannelID:  b.Name(),
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     m.ChannelID,
		IsGroup:    false,
		Content:    content,
		MessageID:  m.ID,
		Timestamp:  m.Timestamp,
	}
	if m.MessageReference != nil {
		im.ReplyTo = m.MessageReference.MessageID
	}
	im.Mentions = discordMentions(m)
	return im
}

// discordMentions normalizes @-mentions from a Discord message. AgentID is left
// empty — the dispatcher resolves it from group membership.
func discordMentions(m *discordgo.MessageCreate) []channel.Mention {
	var mentions []channel.Mention
	for _, u := range m.Mentions {
		if u == nil {
			continue
		}
		mentions = append(mentions, channel.Mention{
			Raw:        "@" + u.Username,
			PlatformID: u.ID,
		})
	}
	return mentions
}

// contains reports whether s is in the list.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
