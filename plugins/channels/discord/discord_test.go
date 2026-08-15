package discord

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/plugins"
)

func TestConfigDecodeRedactSchemaAndValidation(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"token": "secret", "allow_group": true})
	if err != nil || cfg.Token != "secret" || !cfg.AllowGroup || !cfg.AllowDM || cfg.AllowUnlinkedDM || !cfg.RequireMention || cfg.GuestMessageLimitPerMinute != 10 || cfg.GuestMaxPerChannel != 1000 || cfg.GuestRetentionDays != 30 {
		t.Fatalf("DecodeConfig() = %#v, %v", cfg, err)
	}
	cfg, err = DecodeConfig(map[string]any{"token": "secret", "allow_dm": false, "allow_unlinked_dm": true, "require_mention": false})
	if err != nil || cfg.AllowDM || !cfg.AllowUnlinkedDM || cfg.RequireMention {
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
	properties, ok := configSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", configSchema()["properties"])
	}
	unlinked, ok := properties["allow_unlinked_dm"].(map[string]any)
	if !ok || unlinked["default"] != false {
		t.Fatalf("allow_unlinked_dm schema = %#v", properties["allow_unlinked_dm"])
	}
	if validateConfig(channel.DiscordConfig{Token: "secret", GuestMessageLimitPerMinute: 121, GuestMaxPerChannel: 1000, GuestRetentionDays: 30}) == "" {
		t.Fatal("out-of-range guest message limit passed validation")
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
	got := b.incomingMessage(m, nil, "forum", "thread-channel")
	if got.ChatID != "forum" || !got.IsGroup || got.ThreadID != "thread-channel" || got.ReplyTo != "parent" || got.Timestamp.Location() != time.UTC {
		t.Fatalf("incoming message = %#v", got)
	}
	if len(got.Mentions) != 1 || got.Mentions[0].PlatformID != "mentioned" {
		t.Fatalf("mentions = %#v", got.Mentions)
	}
	if text := b.stripBotMention("<@bot> hello <@!bot>"); text != "hello" {
		t.Fatalf("stripped text = %q", text)
	}
}

func TestThreadRouteFailsClosedWhenChannelLookupFails(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channelErr: errors.New("rate limited")}
	_, err = b.resolveMessageRoute(context.Background(), &discordgo.Message{ChannelID: "thread", GuildID: "guild"})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("resolveMessageRoute() error = %v", err)
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
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
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

func TestGuildMessageRejectedWhenGuildsDisabled(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token"}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 0 {
		t.Fatalf("guild message caused %d provisioning calls while allow_group is off", h.calls)
	}
}

func TestGuildMessageRequiresMentionByDefault(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	cfg, err := DecodeConfig(map[string]any{"token": "token", "allow_group": true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Token: cfg.Token, AllowGroup: cfg.AllowGroup, RequireMention: cfg.RequireMention}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
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

func TestGuildReplyToBotSatisfiesMentionRequirement(t *testing.T) {
	h := &unregisteringHandler{}
	b, err := New(Config{Token: "token", AllowGroup: true, RequireMention: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
	m := &discordgo.Message{ID: "reply", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "follow-up", ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: "bot", Bot: true}}}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.handleCalls != 1 {
		t.Fatalf("reply-to-bot handle calls = %d, want 1", h.handleCalls)
	}
}

func TestForumMentionIncludesStarterAndPriorContext(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, RequireMention: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	rest := newFakeDiscordREST()
	rest.channel = &discordgo.Channel{ID: "thread", ParentID: "forum", Type: discordgo.ChannelTypeGuildPublicThread}
	rest.starter = &discordgo.Message{ID: "thread", Content: "How do I deploy this?", Author: &discordgo.User{Username: "op"}}
	rest.messages = []*discordgo.Message{
		{ID: "prior-mentioned", Content: "<@bot> another turn", Author: &discordgo.User{Username: "dave"}, Mentions: []*discordgo.User{{ID: "bot"}}},
		{ID: "prior-2", Content: "The logs show a timeout.", Author: &discordgo.User{Username: "bob"}},
		{ID: "prior-1", Content: "Which environment?", Author: &discordgo.User{Username: "alice"}},
	}
	b.rest = rest
	m := &discordgo.Message{ID: "current", ChannelID: "thread", GuildID: "guild", Author: &discordgo.User{ID: "sender", Username: "carol"}, Mentions: []*discordgo.User{{ID: "bot"}}, Content: "<@bot>"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.groupID != "forum" || h.threadID != "thread" || h.incoming.ChatID != "forum" || h.incoming.ThreadID != "thread" {
		t.Fatalf("thread route = group %q thread %q incoming %#v", h.groupID, h.threadID, h.incoming)
	}
	if len(h.incoming.Content) != 1 || h.incoming.Content[0].(ai.TextContent).Text != "[Mentioned Stella without additional text.]" {
		t.Fatalf("current canonical content = %#v", h.incoming.Content)
	}
	if len(h.history) != 3 {
		t.Fatalf("imported history = %#v", h.history)
	}
	wantHistory := []string{"How do I deploy this?", "Which environment?", "The logs show a timeout."}
	for i, want := range wantHistory {
		got := h.history[i]
		if got.ChatID != "forum" || got.ThreadID != "thread" || got.Content[0].(ai.TextContent).Text != want {
			t.Fatalf("history[%d] = %#v", i, got)
		}
	}
	waitTyping(t, rest)
	if rest.typingCount() != 1 {
		t.Fatalf("typing calls = %d, want immediate acknowledgement", rest.typingCount())
	}
}

func TestThreadContextKeepsStarterAndLatestMessagesWithinBudget(t *testing.T) {
	b, err := New(Config{Token: "token", RequireMention: true}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	rest.starter = &discordgo.Message{ID: "thread", Content: "starter", Author: &discordgo.User{Username: "op"}}
	for i := range threadHistoryLimit {
		rest.messages = append(rest.messages, &discordgo.Message{ID: string(rune('a' + i)), Content: strings.Repeat("x", 2000), Author: &discordgo.User{Username: "user"}})
	}
	b.rest = rest
	history, err := b.loadThreadHistory(context.Background(), &discordgo.Message{ID: "current"}, messageRoute{chatID: "forum", threadID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) >= threadHistoryLimit+1 || history[0].Content[0].(ai.TextContent).Text != "starter" {
		t.Fatalf("bounded history count = %d", len(history))
	}
	total := 0
	for _, message := range history {
		total += len(message.Content[0].(ai.TextContent).Text) + 128
	}
	if total > threadContextMaxLen {
		t.Fatalf("bounded thread context size = %d", total)
	}
}

func TestTypingHeartbeatRenewsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan struct{}, 1)
	var calls atomic.Int32
	go runTypingHeartbeat(ctx, time.Millisecond, func() {
		if calls.Add(1) == 2 {
			called <- struct{}{}
		}
	})
	select {
	case <-called:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("typing heartbeat was not renewed")
	}
}

func TestTypingHeartbeatIsSharedPerChannel(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	stopFirst := b.startTypingHeartbeat("channel")
	stopSecond := b.startTypingHeartbeat("channel")
	waitTyping(t, rest)
	if rest.typingCount() != 1 {
		t.Fatalf("typing calls = %d, want one shared initial call", rest.typingCount())
	}
	b.typingMu.Lock()
	refs := b.typing["channel"].refs
	b.typingMu.Unlock()
	if refs != 2 {
		t.Fatalf("typing refs = %d, want 2", refs)
	}
	stopFirst()
	stopSecond()
}

func TestTypingHeartbeatSendsInitialIndicator(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	stop := b.startTypingHeartbeat("channel")
	waitTyping(t, rest)
	stop()
	if rest.typingCount() != 1 {
		t.Fatalf("typing calls = %d, want 1", rest.typingCount())
	}
}

func TestDeliverStreamCreatesProgressMessageAndEditsFinal(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "final answer"}
	close(events)
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: events}); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.sent) != 1 || rest.sent[0].Content != workingMessage || rest.sent[0].Reference == nil || rest.sent[0].Reference.MessageID != "request" {
		t.Fatalf("progress sends = %#v", rest.sent)
	}
	if len(rest.edited) != 1 || rest.edited[0] != "final answer" {
		t.Fatalf("progress edits = %#v", rest.edited)
	}
}

func TestDeliverStreamDistinguishesAbortFromDeliveryCancellation(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	aborted := make(chan channel.Event, 1)
	aborted <- channel.Event{Err: context.Canceled}
	close(aborted)
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: aborted}); err != nil {
		t.Fatalf("agent abort returned %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.deliverStream(ctx, "channel", "request", &channel.ChatStream{Events: make(chan channel.Event)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("delivery cancellation returned %v", err)
	}
}

func TestDraftDisplayIncludesToolProgressAndStaysBounded(t *testing.T) {
	tracker := &channel.ToolTracker{}
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "go test ./..."})
	display := buildDraftDisplay(strings.Repeat("x", maxMessageLength+100), tracker)
	if len(display) > maxMessageLength || !strings.Contains(display, "bash") || !strings.Contains(display, "go test ./...") || !strings.HasSuffix(display, "▌") {
		t.Fatalf("draft display = %q", display)
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

func TestAttachmentOwnershipIsResolvedBeforeDownload(t *testing.T) {
	h := &rejectingAttachmentHandler{err: agentaccess.ErrForbidden}
	b, err := New(Config{Token: "token", AllowDM: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{
		ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "guest"}, Content: "hello",
		Attachments: []*discordgo.MessageAttachment{{ID: "attachment", Filename: "secret.txt", URL: "https://invalid.example/should-not-be-fetched"}},
	}
	err = b.handleMessage(context.Background(), m)
	if !errors.Is(err, errGuestAttachmentsUnsupported) {
		t.Fatalf("handleMessage() error = %v", err)
	}
	if got := userFacingError(m, err); !strings.Contains(got, "not supported in guest chat") {
		t.Fatalf("userFacingError() = %q", got)
	}
	if h.resolveCalls != 1 || h.handleCalls != 0 {
		t.Fatalf("calls: resolve=%d handle=%d, want 1 and 0", h.resolveCalls, h.handleCalls)
	}
}

func TestAttachmentAdmissionFailureStopsBeforeDownload(t *testing.T) {
	h := &rejectingAttachmentHandler{err: errors.New("storage unavailable")}
	b, err := New(Config{Token: "token", AllowDM: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	m := &discordgo.Message{
		ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "linked-user"}, Content: "hello",
		Attachments: []*discordgo.MessageAttachment{{ID: "attachment", Filename: "image.png", URL: "https://invalid.example/image.png"}},
	}
	if err := b.handleMessage(context.Background(), m); err == nil || !strings.Contains(err.Error(), "storage unavailable") {
		t.Fatalf("handleMessage() error = %v, want storage admission failure", err)
	}
	if h.resolveCalls != 1 || h.handleCalls != 0 {
		t.Fatalf("calls: resolve=%d handle=%d, want 1 and 0", h.resolveCalls, h.handleCalls)
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
	threadID  string
	channelID string
	calls     int
	incoming  channel.IncomingMessage
	history   []channel.IncomingMessage
}

func (h *provisioningHandler) HandleIncoming(_ context.Context, msg channel.IncomingMessage, _, _ string) (string, bool, *channel.ChatStream, error) {
	h.incoming = msg
	return "", false, nil, nil
}

func (h *provisioningHandler) EnsurePlatformGroupMember(_ context.Context, platform, groupID, channelID string) error {
	h.platform = platform
	h.groupID = groupID
	h.channelID = channelID
	h.calls++
	return nil
}

func (h *provisioningHandler) EnsurePlatformThreadGroupMember(_ context.Context, platform, groupID, threadID, channelID string) error {
	h.platform = platform
	h.groupID = groupID
	h.threadID = threadID
	h.channelID = channelID
	h.calls++
	return nil
}

func (h *provisioningHandler) ImportGroupHistory(_ context.Context, messages []channel.IncomingMessage) error {
	h.history = append(h.history, messages...)
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

type rejectingAttachmentHandler struct {
	unregisteringHandler
	err          error
	resolveCalls int
}

func (h *rejectingAttachmentHandler) AdmitAssetSave(context.Context, channel.IncomingMessage) error {
	h.resolveCalls++
	return h.err
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

type fakeDiscordREST struct {
	mu             sync.Mutex
	channel        *discordgo.Channel
	channelErr     error
	starter        *discordgo.Message
	messages       []*discordgo.Message
	typing         int
	typed          chan struct{}
	sent           []*discordgo.MessageSend
	edited         []string
	deletedMessage []string
}

func newFakeDiscordREST() *fakeDiscordREST {
	return &fakeDiscordREST{typed: make(chan struct{}, 16)}
}

func (f *fakeDiscordREST) Channel(string, ...discordgo.RequestOption) (*discordgo.Channel, error) {
	return f.channel, f.channelErr
}

func (f *fakeDiscordREST) ChannelMessage(string, string, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return f.starter, nil
}

func (f *fakeDiscordREST) ChannelMessages(string, int, string, string, string, ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	return f.messages, nil
}

func (f *fakeDiscordREST) ChannelTyping(string, ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing++
	if f.typed != nil {
		select {
		case f.typed <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeDiscordREST) ChannelMessageSendComplex(_ string, message *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, message)
	return &discordgo.Message{ID: "sent-message"}, nil
}

func (f *fakeDiscordREST) ChannelMessageEditComplex(message *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if message.Content != nil {
		f.edited = append(f.edited, *message.Content)
	}
	return &discordgo.Message{ID: message.ID}, nil
}

func (f *fakeDiscordREST) ChannelMessageDelete(channelID, messageID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedMessage = append(f.deletedMessage, channelID+":"+messageID)
	return nil
}

func (f *fakeDiscordREST) typingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typing
}

func waitTyping(t *testing.T, rest *fakeDiscordREST) {
	t.Helper()
	select {
	case <-rest.typed:
	case <-time.After(time.Second):
		t.Fatal("typing indicator was not sent")
	}
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
