package discord

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
)

// newTestBot builds a Bot with a fixed bot user ID for pure-logic tests. New
// only parses the token; no network I/O happens until Start.
func newTestBot(t *testing.T, cfg Config) *Bot {
	return newTestBotH(t, cfg, fakeChannelHandler{})
}

// newTestBotH is newTestBot with a caller-supplied handler (e.g. a spy).
func newTestBotH(t *testing.T, cfg Config, handler channel.Handler) *Bot {
	t.Helper()
	b, err := New(cfg, handler)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b.session.State.User = &discordgo.User{ID: "bot123"}
	return b
}

// spyHandler records whether HandleIncoming was reached, without producing a
// stream (so onMessageCreate returns before any session I/O).
type spyHandler struct{ called bool }

func (s *spyHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	s.called = true
	return "", false, nil, nil
}
func (*spyHandler) ListModels() []channel.ModelOption { return nil }
func (*spyHandler) SwitchModel(string, string) error  { return nil }
func (*spyHandler) ListAgents(context.Context, channel.IncomingMessage) ([]channel.AgentInfo, string, error) {
	return nil, "", nil
}
func (*spyHandler) SwitchAgent(context.Context, channel.IncomingMessage, string) error { return nil }

func msgWith(guildID, channelID string, mentionIDs ...string) *discordgo.MessageCreate {
	m := &discordgo.Message{GuildID: guildID, ChannelID: channelID}
	for _, id := range mentionIDs {
		m.Mentions = append(m.Mentions, &discordgo.User{ID: id})
	}
	return &discordgo.MessageCreate{Message: m}
}

func TestShouldRespond(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		msg  *discordgo.MessageCreate
		want bool
	}{
		{"mention-only, not mentioned", Config{MentionOnly: true}, msgWith("g1", "c1"), false},
		{"mention-only, mentioned", Config{MentionOnly: true}, msgWith("g1", "c1", "bot123"), true},
		{"mention-only, whitelisted channel", Config{MentionOnly: true, RespondChannels: []string{"c1"}}, msgWith("g1", "c1"), true},
		{"mention-only, other whitelist", Config{MentionOnly: true, RespondChannels: []string{"c9"}}, msgWith("g1", "c1"), false},
		{"respond all", Config{MentionOnly: false}, msgWith("g1", "c1"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBot(t, tc.cfg)
			if got := b.shouldRespond(tc.msg); got != tc.want {
				t.Fatalf("shouldRespond = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStripBotMention(t *testing.T) {
	b := newTestBot(t, Config{})
	cases := map[string]string{
		"<@bot123> hello":   "hello",
		"<@!bot123> hi":     "hi",
		"no mention here":   "no mention here",
		"<@other> keep it":  "<@other> keep it",
		"  <@bot123>  spa ": "spa",
	}
	for in, want := range cases {
		if got := b.stripBotMention(in); got != want {
			t.Fatalf("stripBotMention(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIncomingMsgIsAlwaysDirect(t *testing.T) {
	b := newTestBot(t, Config{InstanceID: "disc-1"})
	m := msgWith("g1", "c1")
	m.ID = "m1"
	m.Author = &discordgo.User{ID: "u1", Username: "alice"}
	im := b.incomingMsg(m, channel.TextContent("hey"))

	if im.IsGroup {
		t.Fatal("IsGroup must be false (MVP decision A)")
	}
	if im.Platform != channel.PlatformDiscord {
		t.Fatalf("platform = %q", im.Platform)
	}
	if im.ChatID != "c1" || im.ChannelID != "disc-1" || im.SenderID != "u1" {
		t.Fatalf("incoming = %#v", im)
	}
}

func TestNotifyRequiresTarget(t *testing.T) {
	b := newTestBot(t, Config{})
	if err := b.Notify(t.Context(), channel.Notification{}); err == nil {
		t.Fatal("expected error when no target channel ID")
	}
}

// authored builds a message from a given author with text content.
func authored(guildID, channelID, authorID string, isBot bool, text string, mentionIDs ...string) *discordgo.MessageCreate {
	m := msgWith(guildID, channelID, mentionIDs...)
	m.Author = &discordgo.User{ID: authorID, Username: "u", Bot: isBot}
	m.Content = text
	return m
}

// TestOnMessageCreateGates covers the early-return branches before HandleIncoming:
// loop guard (self/other bots), single-guild filter, the mention gate, and that
// DMs always pass through.
func TestOnMessageCreateGates(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		msg        *discordgo.MessageCreate
		wantcalled bool
	}{
		{"nil author", Config{}, &discordgo.MessageCreate{Message: &discordgo.Message{ChannelID: "c1"}}, false},
		{"self message", Config{}, authored("g1", "c1", "bot123", false, "<@bot123> hi", "bot123"), false},
		{"other bot", Config{}, authored("g1", "c1", "other", true, "hi"), false},
		{"wrong guild", Config{GuildID: "g1"}, authored("g2", "c1", "u1", false, "<@bot123> hi", "bot123"), false},
		{"guild, not mentioned", Config{MentionOnly: true}, authored("g1", "c1", "u1", false, "just chatting"), false},
		{"guild, mentioned", Config{MentionOnly: true}, authored("g1", "c1", "u1", false, "<@bot123> hi", "bot123"), true},
		{"guild, whitelisted channel", Config{MentionOnly: true, RespondChannels: []string{"c1"}}, authored("g1", "c1", "u1", false, "hello"), true},
		{"dm always passes", Config{MentionOnly: true}, authored("", "dm1", "u1", false, "hello"), true},
		{"empty content, no attachments", Config{}, authored("", "dm1", "u1", false, "   "), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spyHandler{}
			b := newTestBotH(t, tc.cfg, spy)
			b.ctx = context.Background()
			b.onMessageCreate(b.session, tc.msg)
			if spy.called != tc.wantcalled {
				t.Fatalf("HandleIncoming called = %v, want %v", spy.called, tc.wantcalled)
			}
		})
	}
}
