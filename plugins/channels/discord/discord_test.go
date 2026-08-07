package discord

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/plugins"
)

func TestConfigDecodeRedactSchemaAndValidation(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"token": "secret", "allowed_guild_ids": "one, two"})
	if err != nil || cfg.Token != "secret" || cfg.AllowedGuildIDs != "one, two" || !cfg.AllowDM || !cfg.RequireMention {
		t.Fatalf("DecodeConfig() = %#v, %v", cfg, err)
	}
	cfg, err = DecodeConfig(map[string]any{"token": "secret", "allow_dm": false, "require_mention": false})
	if err != nil || cfg.AllowDM || cfg.RequireMention {
		t.Fatalf("DecodeConfig(disabled defaults) = %#v, %v", cfg, err)
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
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowedGuildIDs: "other, guild"}, h)
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
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("provisioning calls = %d, want one per Discord channel", h.calls)
	}
}

func TestGuildMessageRejectsUnconfiguredGuild(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowedGuildIDs: "trusted"}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "untrusted", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 0 {
		t.Fatalf("unconfigured guild caused %d provisioning calls", h.calls)
	}
}

func TestGuildMessageRequiresMentionByDefault(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	cfg, err := DecodeConfig(map[string]any{"token": "token", "allowed_guild_ids": "guild"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Token: cfg.Token, AllowedGuildIDs: cfg.AllowedGuildIDs, RequireMention: cfg.RequireMention}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 0 {
		t.Fatalf("unmentioned guild message caused %d provisioning calls", h.calls)
	}
	m.Mentions = []*discordgo.User{{ID: "bot"}}
	m.Content = "<@bot> hello"
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("mentioned guild message caused %d provisioning calls, want 1", h.calls)
	}
}

func TestDirectMessageDoesNotRequireMention(t *testing.T) {
	h := &unregisteringHandler{}
	b, err := New(Config{Token: "token", AllowDM: true, RequireMention: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.handleCalls != 1 {
		t.Fatalf("direct-message handle calls = %d, want 1", h.handleCalls)
	}
}

func TestDirectMessageCanBeDisabled(t *testing.T) {
	h := &unregisteringHandler{}
	b, err := New(Config{Token: "token"}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.handleCalls != 0 {
		t.Fatalf("disabled direct messages caused %d handle calls", h.handleCalls)
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

func TestGuildErrorsDoNotExposeInternalDetails(t *testing.T) {
	err := errors.New("database secret detail")
	if got := userFacingError(&discordgo.Message{GuildID: "guild"}, err); strings.Contains(got, err.Error()) {
		t.Fatalf("guild error exposed internal details: %q", got)
	}
	if got := userFacingError(&discordgo.Message{}, err); !strings.Contains(got, err.Error()) {
		t.Fatalf("direct-message error lost useful detail: %q", got)
	}
}

func TestActivationPrecedesIngressAndFinalizeUnregistersOnce(t *testing.T) {
	h := &unregisteringHandler{}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowDM: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	m := &discordgo.MessageCreate{Message: &discordgo.Message{ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "sender"}, Content: "hello"}}
	b.onMessageCreate(nil, m)
	if h.handleCalls != 0 {
		t.Fatalf("message handled before activation: %d", h.handleCalls)
	}
	if err := b.activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.registerBotCalls != 1 || h.registerPublisherCalls != 1 {
		t.Fatalf("routing registrations = bot %d, publisher %d", h.registerBotCalls, h.registerPublisherCalls)
	}
	b.onMessageCreate(nil, m)
	if h.handleCalls != 1 {
		t.Fatalf("messages handled after activation = %d, want 1", h.handleCalls)
	}
	b.Finalize()
	b.Finalize()
	if err := b.activate(context.Background()); err == nil {
		t.Fatal("activation succeeded after finalization")
	}
	if h.registerBotCalls != 1 || h.registerPublisherCalls != 1 {
		t.Fatalf("routing re-registered after finalization: bot %d, publisher %d", h.registerBotCalls, h.registerPublisherCalls)
	}
	b.onMessageCreate(nil, m)
	if h.handleCalls != 1 {
		t.Fatalf("message handled after finalization: %d", h.handleCalls)
	}
	if h.botCalls != 1 || h.platform != channel.PlatformDiscord || h.botID != "bot" || h.channelID != "discord-main" {
		t.Fatalf("bot unregister = calls %d, platform %q, bot %q, channel %q", h.botCalls, h.platform, h.botID, h.channelID)
	}
	if h.publisherCalls != 1 || h.publisherChannelID != "discord-main" {
		t.Fatalf("publisher unregister = calls %d, channel %q", h.publisherCalls, h.publisherChannelID)
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
	calls     int
}

func (h *provisioningHandler) EnsurePlatformGroupMember(_ context.Context, platform, groupID, channelID string) error {
	h.platform = platform
	h.groupID = groupID
	h.channelID = channelID
	h.calls++
	return nil
}

type unregisteringHandler struct {
	fakeHandler
	handleCalls            int
	registerBotCalls       int
	registerPublisherCalls int
	botCalls               int
	publisherCalls         int
	platform               string
	botID                  string
	channelID              string
	publisherChannelID     string
}

func (h *unregisteringHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	h.handleCalls++
	return "", false, nil, nil
}

func (h *unregisteringHandler) RegisterBotIdentity(string, string, string) {
	h.registerBotCalls++
}

func (h *unregisteringHandler) RegisterGroupPublisher(string, internalchannel.GroupPublisher) {
	h.registerPublisherCalls++
}

func (h *unregisteringHandler) UnregisterBotIdentity(platform, botID, channelID string) {
	h.botCalls++
	h.platform = platform
	h.botID = botID
	h.channelID = channelID
}

func (h *unregisteringHandler) UnregisterGroupPublisher(channelID string) {
	h.publisherCalls++
	h.publisherChannelID = channelID
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
