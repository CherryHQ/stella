package discord

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/plugins"
)

func TestConfigDecodeRedactSchemaAndValidation(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"token": "secret"})
	if err != nil || cfg.Token != "secret" {
		t.Fatalf("DecodeConfig() = %#v, %v", cfg, err)
	}
	redacted := RedactConfig(map[string]any{"token": "secret"})
	if redacted["token"] != "***" {
		t.Fatalf("RedactConfig() = %#v", redacted)
	}
	if validateConfig(channel.DiscordConfig{}) == "" {
		t.Fatal("empty token passed validation")
	}
	if got := configSchema()["required"]; !reflect.DeepEqual(got, []any{"token"}) {
		t.Fatalf("schema required = %#v", got)
	}
}

func TestIncomingMessageNormalizationAndMentionStripping(t *testing.T) {
	b, err := New(Config{InstanceID: "discord-main", Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	ts := time.Date(2026, 8, 5, 12, 30, 0, 0, time.FixedZone("offset", 3600))
	m := &discordgo.Message{ID: "message", ChannelID: "thread-channel", GuildID: "guild", Timestamp: ts, Author: &discordgo.User{ID: "sender", Username: "name"}, Mentions: []*discordgo.User{{ID: "mentioned"}}, MessageReference: &discordgo.MessageReference{MessageID: "parent"}}
	got := b.incomingMessage(m, nil)
	if got.ChatID != "thread-channel" || !got.IsGroup || got.ThreadID != "" || got.ReplyTo != "parent" || got.Timestamp.Location() != time.UTC {
		t.Fatalf("incoming message = %#v", got)
	}
	if len(got.Mentions) != 1 || got.Mentions[0].PlatformID != "mentioned" {
		t.Fatalf("mentions = %#v", got.Mentions)
	}
	if text := b.stripBotMention("<@bot> hello <@!bot>"); text != "hello" {
		t.Fatalf("stripped text = %q", text)
	}
}

func TestAttachmentURLAllowlist(t *testing.T) {
	for _, raw := range []string{"https://cdn.discordapp.com/attachments/1/2/a.png", "https://media.discordapp.net/attachments/1/2/a.png"} {
		if !allowedAttachmentURL(raw) {
			t.Errorf("expected allowed: %s", raw)
		}
	}
	for _, raw := range []string{"http://cdn.discordapp.com/a", "https://cdn.discordapp.com.evil.test/a", "https://evil.test/a", "https://user@cdn.discordapp.com/a"} {
		if allowedAttachmentURL(raw) {
			t.Errorf("expected rejected: %s", raw)
		}
	}
}

func TestGuildMessageEnsuresGroupMembership(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token"}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{
		ID:        "message",
		ChannelID: "discord-channel",
		GuildID:   "guild",
		Author:    &discordgo.User{ID: "sender"},
		Content:   "hello",
	}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.platform != channel.PlatformDiscord || h.groupID != "discord-channel" || h.channelID != "discord-main" {
		t.Fatalf("provisioning call = platform %q, group %q, channel %q", h.platform, h.groupID, h.channelID)
	}
}

func TestChunkingAndAllowedMentions(t *testing.T) {
	chunks := channel.SplitMessage(strings.Repeat("a", maxMessageLength+1), maxMessageLength)
	if len(chunks) != 2 || len(chunks[0]) > maxMessageLength {
		t.Fatalf("chunks = %v", len(chunks))
	}
	if got := noMentions(); got == nil || len(got.Parse) != 0 || len(got.Users) != 0 || len(got.Roles) != 0 {
		t.Fatalf("allowed mentions = %#v", got)
	}
	ref := softReference("channel", "message")
	if ref == nil || ref.FailIfNotExists == nil || *ref.FailIfNotExists {
		t.Fatalf("soft reference = %#v", ref)
	}
}

func TestManagedRuntimeLifecycle(t *testing.T) {
	first := newFakeChannel()
	second := newFakeChannel()
	built := 0
	runtime := NewManagedRuntime(RuntimeDeps{Parent: context.Background(), Handler: fakeHandler{}, NewChannel: func(channel.DiscordConfig, channel.Handler) (channel.Channel, error) {
		built++
		if built == 1 {
			return first, nil
		}
		return second, nil
	}})
	state := plugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"token": "one"}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	wait(t, first.started)
	state.Config["token"] = "two"
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	wait(t, first.stopped)
	wait(t, second.started)
	state.Enabled = false
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	wait(t, second.stopped)
}

type fakeHandler struct{}

func (fakeHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	return "", false, nil, nil
}
func (fakeHandler) ListModels() []channel.ModelOption { return nil }
func (fakeHandler) SwitchModel(string, string) error  { return nil }
func (fakeHandler) ListAgents(context.Context, channel.IncomingMessage) ([]channel.AgentInfo, string, error) {
	return nil, "", nil
}
func (fakeHandler) SwitchAgent(context.Context, channel.IncomingMessage, string) error { return nil }

type provisioningHandler struct {
	fakeHandler
	platform  string
	groupID   string
	channelID string
}

func (h *provisioningHandler) EnsurePlatformGroupMember(_ context.Context, platform, groupID, channelID string) error {
	h.platform = platform
	h.groupID = groupID
	h.channelID = channelID
	return nil
}

type fakeChannel struct{ started, stopped chan struct{} }

func newFakeChannel() *fakeChannel { return &fakeChannel{make(chan struct{}), make(chan struct{})} }
func (*fakeChannel) Name() string  { return channel.PlatformDiscord }
func (f *fakeChannel) Start(ctx context.Context) error {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	return ctx.Err()
}
func (*fakeChannel) Stop()                                              {}
func (*fakeChannel) Notify(context.Context, channel.Notification) error { return nil }
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}
