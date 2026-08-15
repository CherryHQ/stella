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
	"unicode/utf8"

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
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowAllGuilds: true}, h)
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

func TestConfigDecodeNormalizesAllowlists(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"token": "secret", "allow_group": true, "allow_all_guilds": true,
		"allowed_guild_ids":   []any{" guild-1 ", "guild-1", "", "guild-2"},
		"allowed_channel_ids": []any{"chan-1", " ", "chan-1"},
		"allowed_user_ids":    []any{"user-1"},
		"allowed_role_ids":    []any{"role-1", "role-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowAllGuilds {
		t.Fatal("allow_all_guilds not decoded")
	}
	if got := cfg.AllowedGuildIDs; !reflect.DeepEqual(got, []string{"guild-1", "guild-2"}) {
		t.Fatalf("allowed_guild_ids = %#v, want trimmed and deduplicated", got)
	}
	if got := cfg.AllowedChannelIDs; !reflect.DeepEqual(got, []string{"chan-1"}) {
		t.Fatalf("allowed_channel_ids = %#v, want blank entries dropped and deduplicated", got)
	}
	if got := cfg.AllowedUserIDs; !reflect.DeepEqual(got, []string{"user-1"}) {
		t.Fatalf("allowed_user_ids = %#v", got)
	}
	if got := cfg.AllowedRoleIDs; !reflect.DeepEqual(got, []string{"role-1"}) {
		t.Fatalf("allowed_role_ids = %#v, want deduplicated", got)
	}
}

func TestGuildAccessFailsClosedByDefaultEvenWithAllowGroupOn(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 0 || h.incoming.MessageID != "" {
		t.Fatalf("allow_group on with an empty allowlist and allow_all_guilds off still served: calls=%d incoming=%#v", h.calls, h.incoming)
	}
	if rest.typingCount() != 0 {
		t.Fatalf("typing indicator sent for a message denied before the fail-closed gate: %d", rest.typingCount())
	}
}

func TestGuildAccessAllowedByGuildID(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowedGuildIDs: []string{"guild"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 || h.groupID != "discord-channel" {
		t.Fatalf("guild allowlist match not served: calls=%d group=%q", h.calls, h.groupID)
	}
}

func TestGuildAccessAllowedByChannelID(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowedChannelIDs: []string{"discord-channel"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "unlisted-guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 || h.groupID != "discord-channel" {
		t.Fatalf("channel allowlist match not served: calls=%d group=%q", h.calls, h.groupID)
	}
}

func TestGuildAccessAllowedByThreadParentChannelID(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowedChannelIDs: []string{"forum-parent"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "thread-channel", ParentID: "forum-parent", Type: discordgo.ChannelTypeGuildPublicThread}}
	m := &discordgo.Message{ID: "message", ChannelID: "thread-channel", GuildID: "unlisted-guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 || h.groupID != "forum-parent" || h.threadID != "thread-channel" {
		t.Fatalf("parent-channel allowlist match not served: calls=%d group=%q thread=%q", h.calls, h.groupID, h.threadID)
	}
}

func TestGuildAccessAllowedByUserID(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowedUserIDs: []string{"sender"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "unlisted-guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("user allowlist match not served: calls=%d", h.calls)
	}
}

func TestGuildAccessAllowedByRoleID(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowedRoleIDs: []string{"role-mod"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "unlisted-guild", Author: &discordgo.User{ID: "sender"}, Member: &discordgo.Member{Roles: []string{"role-member", "role-mod"}}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("role allowlist match not served: calls=%d", h.calls)
	}
}

func TestGuildAccessDeniedWhenNoAllowlistMatches(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowedGuildIDs: []string{"other-guild"}, AllowedChannelIDs: []string{"other-channel"}, AllowedUserIDs: []string{"other-user"}, AllowedRoleIDs: []string{"other-role"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	rest.channel = &discordgo.Channel{ID: "thread-channel", ParentID: "other-forum-parent", Type: discordgo.ChannelTypeGuildPublicThread}
	b.rest = rest
	m := &discordgo.Message{ID: "message", ChannelID: "thread-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Member: &discordgo.Member{Roles: []string{"role-member"}}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 0 || h.incoming.MessageID != "" {
		t.Fatalf("message outside every allowlist was served: calls=%d incoming=%#v", h.calls, h.incoming)
	}
	if rest.typingCount() != 0 {
		t.Fatalf("typing indicator sent for an allowlist-denied message: %d", rest.typingCount())
	}
}

func TestGuildAccessAllowAllGuildsBypassesEmptyAllowlist(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowAllGuilds: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.rest = &fakeDiscordREST{channel: &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}}
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "any-unlisted-guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("allow_all_guilds did not bypass an empty allowlist: calls=%d", h.calls)
	}
}

func TestGuildMessageRequiresMentionByDefault(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	cfg, err := DecodeConfig(map[string]any{"token": "token", "allow_group": true, "allow_all_guilds": true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Token: cfg.Token, AllowGroup: cfg.AllowGroup, AllowAllGuilds: cfg.AllowAllGuilds, RequireMention: cfg.RequireMention}, h)
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
	b, err := New(Config{Token: "token", AllowGroup: true, AllowAllGuilds: true, RequireMention: true}, h)
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
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowAllGuilds: true, RequireMention: true}, h)
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
	if h.legacyGroupID != "thread" {
		t.Fatalf("legacy group hint = %q, want the thread's own ID for pre-migration adoption", h.legacyGroupID)
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
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: events}, nil, textDelivery{}); err != nil {
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
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: aborted}, nil, textDelivery{}); err != nil {
		t.Fatalf("agent abort returned %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.deliverStream(ctx, "channel", "request", &channel.ChatStream{Events: make(chan channel.Event)}, nil, textDelivery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("delivery cancellation returned %v", err)
	}
}

func TestDraftDisplayIncludesToolProgressAndStaysBounded(t *testing.T) {
	tracker := &channel.ToolTracker{}
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "go test ./..."})
	display := buildDraftDisplay(strings.Repeat("x", maxMessageLength+100), tracker)
	if utf8.RuneCountInString(display) > maxMessageLength || !strings.Contains(display, "bash") || !strings.Contains(display, "go test ./...") || !strings.HasSuffix(display, "▌") {
		t.Fatalf("draft display = %q", display)
	}
}

// TestDraftDisplayCJKStaysWithinRuneBudget guards the 2000-character draft
// limit against byte-length regressions: three-byte Chinese characters would
// blow past 2000 bytes long before 2000 runes, so the truncation must count
// runes, not bytes, and must never split a rune's UTF-8 encoding.
func TestDraftDisplayCJKStaysWithinRuneBudget(t *testing.T) {
	text := strings.Repeat("中文测试消息内容", maxMessageLength) // far more than maxMessageLength runes
	display := buildDraftDisplay(text, &channel.ToolTracker{})
	if n := utf8.RuneCountInString(display); n > maxMessageLength {
		t.Fatalf("draft display has %d runes, want <= %d", n, maxMessageLength)
	}
	if !utf8.ValidString(display) {
		t.Fatalf("draft display is not valid UTF-8: %q", display)
	}
	if !strings.HasSuffix(display, "▌") {
		t.Fatalf("draft display = %q, want suffix ▌", display)
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
	chunks := channel.SplitMarkdown(strings.Repeat("a", maxMessageLength+1), maxMessageLength)
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
	platform      string
	groupID       string
	threadID      string
	legacyGroupID string
	channelID     string
	calls         int
	incoming      channel.IncomingMessage
	history       []channel.IncomingMessage
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

func (h *provisioningHandler) EnsurePlatformThreadGroupMember(_ context.Context, platform, groupID, threadID, legacyGroupID, channelID string) error {
	h.platform = platform
	h.groupID = groupID
	h.threadID = threadID
	h.legacyGroupID = legacyGroupID
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
	mu               sync.Mutex
	channel          *discordgo.Channel
	channelErr       error
	starter          *discordgo.Message
	messages         []*discordgo.Message
	typing           int
	typed            chan struct{}
	sent             []*discordgo.MessageSend
	sendErr          func(n int, m *discordgo.MessageSend) error
	edited           []string
	editedRequests   []*discordgo.MessageEdit
	deletedMessage   []string
	reactionsAdded   []string
	reactionsRemoved []string
	reactionErr      error
	bulkOverwriteErr error
	bulkOverwriteIDs []string
	interactionAcks  []*discordgo.InteractionResponse
	interactionEdits []*discordgo.WebhookEdit
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
	if f.sendErr != nil {
		if err := f.sendErr(len(f.sent)+1, message); err != nil {
			return nil, err
		}
	}
	f.sent = append(f.sent, message)
	return &discordgo.Message{ID: "sent-message"}, nil
}

// sentContents returns the content of every message the bot sent, in order.
func (f *fakeDiscordREST) sentContents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sent))
	for _, m := range f.sent {
		out = append(out, m.Content)
	}
	return out
}

func (f *fakeDiscordREST) ChannelMessageEditComplex(message *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if message.Content != nil {
		f.edited = append(f.edited, *message.Content)
	}
	f.editedRequests = append(f.editedRequests, message)
	return &discordgo.Message{ID: message.ID}, nil
}

func (f *fakeDiscordREST) ChannelMessageDelete(channelID, messageID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedMessage = append(f.deletedMessage, channelID+":"+messageID)
	return nil
}

func (f *fakeDiscordREST) MessageReactionAdd(channelID, messageID, emojiID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactionsAdded = append(f.reactionsAdded, channelID+":"+messageID+":"+emojiID)
	return f.reactionErr
}

func (f *fakeDiscordREST) MessageReactionRemove(channelID, messageID, emojiID, userID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactionsRemoved = append(f.reactionsRemoved, channelID+":"+messageID+":"+emojiID+":"+userID)
	return f.reactionErr
}

func (f *fakeDiscordREST) ApplicationCommandBulkOverwrite(_, _ string, commands []*discordgo.ApplicationCommand, _ ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bulkOverwriteErr != nil {
		return nil, f.bulkOverwriteErr
	}
	for _, c := range commands {
		f.bulkOverwriteIDs = append(f.bulkOverwriteIDs, c.Name)
	}
	return commands, nil
}

func (f *fakeDiscordREST) InteractionRespond(_ *discordgo.Interaction, resp *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactionAcks = append(f.interactionAcks, resp)
	return nil
}

func (f *fakeDiscordREST) InteractionResponseEdit(_ *discordgo.Interaction, newresp *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactionEdits = append(f.interactionEdits, newresp)
	return &discordgo.Message{ID: "interaction-message"}, nil
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

type respondingHandler struct {
	fakeHandler
	resp    string
	handled bool
	err     error
}

func (h *respondingHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	return h.resp, h.handled, nil, h.err
}

type interactionCaptureHandler struct {
	fakeHandler
	msg     channel.IncomingMessage
	cmd     string
	args    string
	calls   int
	resp    string
	handled bool
	err     error
}

func (h *interactionCaptureHandler) HandleIncoming(_ context.Context, msg channel.IncomingMessage, cmd, args string) (string, bool, *channel.ChatStream, error) {
	h.calls++
	h.msg = msg
	h.cmd = cmd
	h.args = args
	return h.resp, h.handled, nil, h.err
}

func extractButtonCustomID(t *testing.T, rest *fakeDiscordREST) string {
	t.Helper()
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.sent) == 0 {
		t.Fatal("no discord message was sent")
	}
	msg := rest.sent[len(rest.sent)-1]
	for _, c := range msg.Components {
		row, ok := c.(discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, inner := range row.Components {
			if btn, ok := inner.(discordgo.Button); ok {
				return btn.CustomID
			}
		}
	}
	t.Fatal("no cancel button found in the sent message")
	return ""
}

func TestRegisterNativeCommandsIsBestEffort(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	b.botID = "bot-app-id"
	b.registerNativeCommands(context.Background())
	rest.mu.Lock()
	names := append([]string(nil), rest.bulkOverwriteIDs...)
	rest.mu.Unlock()
	want := []string{"help", "start", "new", "compact", "abort", "whoami", "link"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("registered command names = %#v, want %#v", names, want)
	}

	// A registration failure is warn-only: it must not panic, error, or
	// otherwise disrupt startup — text commands remain available regardless.
	failing := newFakeDiscordREST()
	failing.bulkOverwriteErr = errors.New("rate limited")
	b.rest = failing
	b.registerNativeCommands(context.Background())
}

func TestNativeCommandsAreScopedToBotDMOnly(t *testing.T) {
	for _, cmd := range nativeCommands() {
		if cmd.Contexts == nil {
			t.Fatalf("command %q has no Contexts restriction, want [InteractionContextBotDM] only", cmd.Name)
		}
		want := []discordgo.InteractionContextType{discordgo.InteractionContextBotDM}
		if !reflect.DeepEqual(*cmd.Contexts, want) {
			t.Fatalf("command %q Contexts = %#v, want %#v", cmd.Name, *cmd.Contexts, want)
		}
	}
}

func TestCommandInteractionDefersAcksThenEditsWithParsedOption(t *testing.T) {
	h := &interactionCaptureHandler{resp: "Account linked successfully!", handled: true}
	b, err := New(Config{Token: "token", AllowDM: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	ix := &discordgo.Interaction{
		ID: "interaction-1", Type: discordgo.InteractionApplicationCommand,
		User: &discordgo.User{ID: "sender", Username: "carol"}, ChannelID: "dm",
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "link",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "code", Type: discordgo.ApplicationCommandOptionString, Value: "ABCD1234"},
			},
		},
	}
	b.handleCommandInteraction(context.Background(), ix)

	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.interactionAcks) != 1 {
		t.Fatalf("acks = %d, want 1", len(rest.interactionAcks))
	}
	ack := rest.interactionAcks[0]
	if ack.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource || ack.Data == nil || ack.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("ack = %#v, want a deferred ephemeral response", ack)
	}
	if len(rest.interactionEdits) != 1 || rest.interactionEdits[0].Content == nil || *rest.interactionEdits[0].Content != "Account linked successfully!" {
		t.Fatalf("edits = %#v", rest.interactionEdits)
	}
	if h.cmd != "/link" || h.args != "ABCD1234" {
		t.Fatalf("captured command = %q, args = %q", h.cmd, h.args)
	}
	if len(h.msg.Content) != 1 || h.msg.Content[0].(ai.TextContent).Text != "/link ABCD1234" {
		t.Fatalf("captured content = %#v", h.msg.Content)
	}
	if h.msg.SenderID != "sender" || h.msg.IsGroup {
		t.Fatalf("captured msg = %#v", h.msg)
	}
}

func TestCommandInteractionEditTruncatesOverlongReplyRuneSafe(t *testing.T) {
	cases := []struct {
		name string
		resp string
	}{
		{"ASCII", strings.Repeat("a", maxMessageLength+500)},
		{"CJK", strings.Repeat("测", maxMessageLength+500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &interactionCaptureHandler{resp: tc.resp, handled: true}
			b, err := New(Config{Token: "token", AllowDM: true}, h)
			if err != nil {
				t.Fatal(err)
			}
			rest := newFakeDiscordREST()
			b.rest = rest
			ix := &discordgo.Interaction{
				ID: "interaction-1", Type: discordgo.InteractionApplicationCommand,
				User: &discordgo.User{ID: "sender"}, ChannelID: "dm",
				Data: discordgo.ApplicationCommandInteractionData{Name: "help"},
			}
			b.handleCommandInteraction(context.Background(), ix)

			rest.mu.Lock()
			defer rest.mu.Unlock()
			if len(rest.interactionEdits) != 1 || rest.interactionEdits[0].Content == nil {
				t.Fatalf("edits = %#v", rest.interactionEdits)
			}
			edited := *rest.interactionEdits[0].Content
			if n := utf8.RuneCountInString(edited); n > maxMessageLength {
				t.Fatalf("edited content has %d runes, want <= %d (Discord rejects the edit above that, leaving \"thinking…\" stuck)", n, maxMessageLength)
			}
			if !strings.Contains(edited, "truncated") {
				t.Fatalf("edited content = %q, want an explicit truncation marker", edited)
			}
		})
	}
}

func TestCommandInteractionDeniedOutsideAllowlistNeverCallsHandler(t *testing.T) {
	h := &interactionCaptureHandler{resp: "should not be used", handled: true}
	b, err := New(Config{Token: "token", AllowGroup: true, AllowedGuildIDs: []string{"other-guild"}}, h)
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	ix := &discordgo.Interaction{
		ID: "interaction-1", Type: discordgo.InteractionApplicationCommand,
		GuildID: "guild", ChannelID: "discord-channel",
		Member: &discordgo.Member{User: &discordgo.User{ID: "sender"}},
		Data:   discordgo.ApplicationCommandInteractionData{Name: "whoami"},
	}
	b.handleCommandInteraction(context.Background(), ix)
	if h.calls != 0 {
		t.Fatalf("handler calls = %d, want 0 for a guild outside the allowlist", h.calls)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.interactionEdits) != 1 || rest.interactionEdits[0].Content == nil || !strings.Contains(*rest.interactionEdits[0].Content, "not available") {
		t.Fatalf("edits = %#v", rest.interactionEdits)
	}
}

func TestReactionMarksReceivedThenSuccessOnHandledCommand(t *testing.T) {
	h := &respondingHandler{resp: "pong", handled: true}
	b, err := New(Config{Token: "token", AllowDM: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	m := &discordgo.Message{ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "sender"}, Content: "/whoami"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.reactionsAdded, []string{"dm:message:👀", "dm:message:✅"}) {
		t.Fatalf("reactions added = %#v", rest.reactionsAdded)
	}
	if !reflect.DeepEqual(rest.reactionsRemoved, []string{"dm:message:👀:@me", "dm:message:❌:@me"}) {
		t.Fatalf("reactions removed = %#v, want the ack and opposite terminal reaction removed via @me (not remove-all)", rest.reactionsRemoved)
	}
}

func TestReactionFailureTransitionAndReactionRestFailureIsIgnored(t *testing.T) {
	h := &respondingHandler{err: errors.New("boom")}
	b, err := New(Config{Token: "token", AllowDM: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	// Even a broken reaction API must not change the outcome: no retry, no
	// swallowed handler error.
	rest.reactionErr = errors.New("discord reaction api down")
	b.rest = rest
	m := &discordgo.Message{ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	err = b.handleMessage(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("handleMessage() error = %v, want the handler error surfaced despite reaction failures", err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.reactionsAdded, []string{"dm:message:👀", "dm:message:❌"}) {
		t.Fatalf("reactions added = %#v", rest.reactionsAdded)
	}
}

func TestReactionStaysPendingForAsyncGroupMessageUntilPublish(t *testing.T) {
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowAllGuilds: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	rest := newFakeDiscordREST()
	rest.channel = &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}
	b.rest = rest
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "<@bot> hello", Mentions: []*discordgo.User{{ID: "bot"}}}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	if !reflect.DeepEqual(rest.reactionsAdded, []string{"discord-channel:message:👀"}) || len(rest.reactionsRemoved) != 0 {
		rest.mu.Unlock()
		t.Fatalf("reactions added = %#v, removed = %#v; want only the pending ack until Publish finalizes", rest.reactionsAdded, rest.reactionsRemoved)
	}
	rest.mu.Unlock()
}

func TestReactionLifecycleDeniedForAmbientMessageWithRequireMentionFalse(t *testing.T) {
	// With require_mention off, an unaddressed group message (no mention, no
	// reply-to-bot) is still processed for ambient participation, but it
	// never opted into the 👀/✅/❌ lifecycle: no 👀 must ever be added, so
	// none is ever left stale.
	h := &provisioningHandler{fakeHandler: fakeHandler{}}
	b, err := New(Config{InstanceID: "discord-main", Token: "token", AllowGroup: true, AllowAllGuilds: true, RequireMention: false}, h)
	if err != nil {
		t.Fatal(err)
	}
	b.session.State.User = &discordgo.User{ID: "bot"}
	rest := newFakeDiscordREST()
	rest.channel = &discordgo.Channel{ID: "discord-channel", Type: discordgo.ChannelTypeGuildText}
	b.rest = rest
	m := &discordgo.Message{ID: "message", ChannelID: "discord-channel", GuildID: "guild", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	if err := b.handleMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.reactionsAdded) != 0 || len(rest.reactionsRemoved) != 0 {
		t.Fatalf("reactions added = %#v, removed = %#v; want no reaction lifecycle for an ambient message", rest.reactionsAdded, rest.reactionsRemoved)
	}
}

func TestPublishFinishesReactionOnTriggeringMessageAcrossReplyTo(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "group reply"}
	close(events)
	req := internalchannel.GroupPublishRequest{
		PlatformGroupID:   "group-channel",
		ReplyTo:           "trigger-message",
		LifecycleFeedback: true,
		Stream:            &channel.ChatStream{Events: events},
	}
	if err := b.Publish(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.reactionsAdded, []string{"group-channel:trigger-message:✅"}) {
		t.Fatalf("reactions added = %#v", rest.reactionsAdded)
	}
	if !reflect.DeepEqual(rest.reactionsRemoved, []string{"group-channel:trigger-message:👀:@me", "group-channel:trigger-message:❌:@me"}) {
		t.Fatalf("reactions removed = %#v", rest.reactionsRemoved)
	}
}

func TestPublishDoesNotReactToAmbientTrigger(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "ambient reply"}
	close(events)
	if err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "group-channel",
		ReplyTo:         "ambient-message",
		Stream:          &channel.ChatStream{Events: events},
	}); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.reactionsAdded) != 0 || len(rest.reactionsRemoved) != 0 {
		t.Fatalf("ambient publish reactions added=%#v removed=%#v", rest.reactionsAdded, rest.reactionsRemoved)
	}
}

func TestFinishReactionRemovesOppositeTerminalOnFailThenSuccessRetransition(t *testing.T) {
	// A redelivered group turn can call finishReaction twice for the same
	// message: once on a failed attempt, once on a successful retry. The
	// stale ❌ must be cleared before ✅ lands, or both would sit on the
	// message together.
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	b.finishReaction(context.Background(), "chan", "msg", false)
	b.finishReaction(context.Background(), "chan", "msg", true)
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.reactionsAdded, []string{"chan:msg:❌", "chan:msg:✅"}) {
		t.Fatalf("reactions added = %#v", rest.reactionsAdded)
	}
	if !reflect.DeepEqual(rest.reactionsRemoved, []string{
		"chan:msg:👀:@me", "chan:msg:✅:@me",
		"chan:msg:👀:@me", "chan:msg:❌:@me",
	}) {
		t.Fatalf("reactions removed = %#v, want the ack and each transition's opposite terminal cleared via @me", rest.reactionsRemoved)
	}
}

func TestCancelButtonRequesterCanAbort(t *testing.T) {
	b, err := New(Config{Token: "token", AllowDM: true}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	aborted := make(chan struct{}, 1)
	draft := b.beginDraft(context.Background(), "dm", "reply", &cancelControl{
		requesterID: "requester",
		abort:       func() bool { aborted <- struct{}{}; return true },
	})
	if draft == nil || draft.cancelToken == "" {
		t.Fatalf("beginDraft() = %#v, want a registered cancel token", draft)
	}
	customID := extractButtonCustomID(t, rest)

	ix := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		User: &discordgo.User{ID: "requester"},
		Data: discordgo.MessageComponentInteractionData{CustomID: customID, ComponentType: discordgo.ButtonComponent},
	}
	b.handleComponentInteraction(context.Background(), ix)

	wait(t, aborted)
	rest.mu.Lock()
	ack := rest.interactionAcks[0]
	rest.mu.Unlock()
	if ack.Type != discordgo.InteractionResponseUpdateMessage || ack.Data.Content != "Stopping…" || len(ack.Data.Components) != 0 {
		t.Fatalf("ack = %#v, want an update clearing components", ack)
	}
	if _, ok := b.cancels.get(strings.TrimPrefix(customID, cancelCustomIDPrefix)); ok {
		t.Fatal("cancel token still registered after use")
	}
}

func TestCancelButtonRejectsNonRequester(t *testing.T) {
	b, err := New(Config{Token: "token", AllowDM: true}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	aborted := false
	b.beginDraft(context.Background(), "dm", "reply", &cancelControl{
		requesterID: "requester",
		abort:       func() bool { aborted = true; return true },
	})
	customID := extractButtonCustomID(t, rest)

	ix := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		User: &discordgo.User{ID: "someone-else"},
		Data: discordgo.MessageComponentInteractionData{CustomID: customID, ComponentType: discordgo.ButtonComponent},
	}
	b.handleComponentInteraction(context.Background(), ix)

	if aborted {
		t.Fatal("abort invoked for a non-requester click")
	}
	rest.mu.Lock()
	ack := rest.interactionAcks[0]
	rest.mu.Unlock()
	if ack.Type != discordgo.InteractionResponseChannelMessageWithSource || ack.Data.Flags&discordgo.MessageFlagsEphemeral == 0 || !strings.Contains(ack.Data.Content, "Only the requester") {
		t.Fatalf("ack = %#v", ack)
	}
	if _, ok := b.cancels.get(strings.TrimPrefix(customID, cancelCustomIDPrefix)); !ok {
		t.Fatal("cancel token should remain registered after a rejected click")
	}
}

func TestCancelButtonUnknownTokenRespondsEnded(t *testing.T) {
	b, err := New(Config{Token: "token", AllowDM: true}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	ix := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		User: &discordgo.User{ID: "someone"},
		Data: discordgo.MessageComponentInteractionData{CustomID: cancelCustomIDPrefix + "does-not-exist", ComponentType: discordgo.ButtonComponent},
	}
	b.handleComponentInteraction(context.Background(), ix)
	rest.mu.Lock()
	ack := rest.interactionAcks[0]
	rest.mu.Unlock()
	if !strings.Contains(ack.Data.Content, "already ended") {
		t.Fatalf("ack = %#v, want an already-ended response for an unknown (e.g. post-restart orphan) token", ack)
	}
}

func TestCancelButtonDeniedOutsideAllowlistBeforeRequesterCheck(t *testing.T) {
	b, err := New(Config{Token: "token", AllowGroup: true, AllowedGuildIDs: []string{"other-guild"}}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	aborted := false
	b.beginDraft(context.Background(), "discord-channel", "reply", &cancelControl{
		requesterID: "requester",
		abort:       func() bool { aborted = true; return true },
	})
	customID := extractButtonCustomID(t, rest)
	ix := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent, GuildID: "guild", ChannelID: "discord-channel",
		Member: &discordgo.Member{User: &discordgo.User{ID: "requester"}},
		Data:   discordgo.MessageComponentInteractionData{CustomID: customID, ComponentType: discordgo.ButtonComponent},
	}
	b.handleComponentInteraction(context.Background(), ix)
	if aborted {
		t.Fatal("abort invoked from a guild outside the allowlist, even though the clicking user was the requester")
	}
	rest.mu.Lock()
	ack := rest.interactionAcks[0]
	rest.mu.Unlock()
	if !strings.Contains(ack.Data.Content, "not available") {
		t.Fatalf("ack = %#v", ack)
	}
}

func TestDeliverStreamFinalizeClearsCancelButtonAndUnregistersToken(t *testing.T) {
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "final answer"}
	close(events)
	cancel := &cancelControl{requesterID: "requester", abort: func() bool { return true }}
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: events}, cancel, textDelivery{}); err != nil {
		t.Fatal(err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.editedRequests) == 0 {
		t.Fatal("no edit recorded")
	}
	final := rest.editedRequests[len(rest.editedRequests)-1]
	if final.Components == nil || len(*final.Components) != 0 {
		t.Fatalf("final edit components = %#v, want a cleared (non-nil, empty) slice", final.Components)
	}
}

// threeChunkText returns text that SplitMarkdown deterministically splits into
// three Discord-sized chunks, plus those chunks. Chunk indices are the unit the
// durable delivery cursor counts in, so a resume test is only meaningful
// against a text whose split is pinned.
func threeChunkText(t *testing.T) (string, []string) {
	t.Helper()
	text := strings.Repeat("a", 1500) + "\n" + strings.Repeat("b", 1500) + "\n" + strings.Repeat("c", 1500)
	chunks := channel.SplitMarkdown(text, maxMessageLength)
	if len(chunks) != 3 {
		t.Fatalf("fixture split into %d chunks, want 3", len(chunks))
	}
	return text, chunks
}

func newPublishingBot(t *testing.T) (*Bot, *fakeDiscordREST) {
	t.Helper()
	b, err := New(Config{Token: "token"}, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	rest := newFakeDiscordREST()
	b.rest = rest
	return b, rest
}

// TestPublishRecordsResponseBeforeTheFirstChunkIsSent closes the crash window.
// Discord cannot send a chunk before it has buffered the whole response, so the
// response is persisted the moment the stream ends and before anything reaches
// the channel: a crash in between then costs a re-delivery, not a second agent
// turn. The recorded text is the exact string that gets split, which is what
// makes a chunk index mean the same thing on the retry.
func TestPublishRecordsResponseBeforeTheFirstChunkIsSent(t *testing.T) {
	b, rest := newPublishingBot(t)
	text, chunks := threeChunkText(t)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: text}
	close(events)
	var recorded string
	var sentAtRecord []string
	editedAtRecord := -1
	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "group-channel",
		ReplyTo:         "trigger-message",
		Stream:          &channel.ChatStream{Events: events},
		RecordResult: func(_ context.Context, response string) error {
			recorded = response
			sentAtRecord = rest.sentContents()
			rest.mu.Lock()
			editedAtRecord = len(rest.edited)
			rest.mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if recorded != text {
		t.Fatalf("recorded %d bytes, want the exact %d-byte text that gets chunked", len(recorded), len(text))
	}
	if !reflect.DeepEqual(sentAtRecord, []string{workingMessage}) || editedAtRecord != 0 {
		t.Fatalf("at record time sent %#v with %d edits, want only the progress draft", sentAtRecord, editedAtRecord)
	}
	if got := rest.sentContents(); !reflect.DeepEqual(got, append([]string{workingMessage}, chunks...)) {
		t.Fatalf("sent %d messages, want the draft followed by all three chunks", len(got))
	}
}

// A response Discord could not persist must not be delivered at all: the retry
// would find no record, run the agent again, and answer a group that had
// already read part of the first answer.
func TestPublishRecordFailureSendsNothing(t *testing.T) {
	b, rest := newPublishingBot(t)
	text, _ := threeChunkText(t)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: text}
	close(events)
	lost := errors.New("set dispatch result message: lost dispatch ownership")
	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "group-channel",
		Stream:          &channel.ChatStream{Events: events},
		RecordResult:    func(context.Context, string) error { return lost },
		MarkDelivered: func(context.Context, int64) error {
			t.Error("confirmed a chunk that was never sent")
			return nil
		},
	})
	if !errors.Is(err, lost) {
		t.Fatalf("publish error = %v, want the failed record", err)
	}
	if got := rest.sentContents(); !reflect.DeepEqual(got, []string{workingMessage}) {
		t.Fatalf("sent %#v, want nothing beyond the progress draft", got)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.deletedMessage, []string{"group-channel:sent-message"}) {
		t.Fatalf("deleted drafts = %#v, want the unrecorded draft removed", rest.deletedMessage)
	}
}

// TestPublishResumesPersistedTextFromDeliveryCursor is the payoff of durable
// delivery: a re-delivery sends only the chunks the cursor has not confirmed,
// with no agent stream, no progress draft, and no cancel button.
func TestPublishResumesPersistedTextFromDeliveryCursor(t *testing.T) {
	b, rest := newPublishingBot(t)
	text, chunks := threeChunkText(t)
	var confirmed []int64
	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "group-channel",
		ReplyTo:         "trigger-message",
		Text:            text,
		DeliveryCursor:  1,
		MarkDelivered: func(_ context.Context, n int64) error {
			confirmed = append(confirmed, n)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("re-delivery: %v", err)
	}
	if got := rest.sentContents(); !reflect.DeepEqual(got, chunks[1:]) {
		t.Fatalf("re-delivery sent %d messages, want the two unconfirmed chunks", len(got))
	}
	if !reflect.DeepEqual(confirmed, []int64{2, 3}) {
		t.Fatalf("confirmed cursors = %v, want [2 3]", confirmed)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if len(rest.editedRequests) != 0 {
		t.Fatalf("re-delivery edited %d messages, want no progress draft", len(rest.editedRequests))
	}
}

// TestPublishStopsAtFailedChunkAndLeavesNoStaleDraft pins the failure half: the
// cursor keeps the chunks that landed, delivery stops at the first failure, and
// the scratch draft is removed rather than left showing a retry notice that the
// retry itself would never clean up.
func TestPublishStopsAtFailedChunkAndLeavesNoStaleDraft(t *testing.T) {
	b, rest := newPublishingBot(t)
	text, chunks := threeChunkText(t)
	boom := errors.New("discord 500")
	rest.sendErr = func(_ int, m *discordgo.MessageSend) error {
		if m.Content == chunks[1] {
			return boom
		}
		return nil
	}
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: text}
	close(events)
	var confirmed []int64
	err := b.Publish(context.Background(), internalchannel.GroupPublishRequest{
		PlatformGroupID: "group-channel",
		ReplyTo:         "trigger-message",
		Stream:          &channel.ChatStream{Events: events},
		MarkDelivered: func(_ context.Context, n int64) error {
			confirmed = append(confirmed, n)
			return nil
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("publish error = %v, want the failed chunk send", err)
	}
	if !reflect.DeepEqual(confirmed, []int64{1}) {
		t.Fatalf("confirmed cursors = %v, want [1]: only the chunk Discord accepted", confirmed)
	}
	if got := rest.sentContents(); !reflect.DeepEqual(got, []string{workingMessage, chunks[0]}) {
		t.Fatalf("sent = %d messages, want the draft and the first chunk only", len(got))
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.deletedMessage, []string{"group-channel:sent-message"}) {
		t.Fatalf("deleted drafts = %#v, want the scratch draft removed", rest.deletedMessage)
	}
	for _, edit := range rest.edited {
		if strings.Contains(edit, "⚠️") {
			t.Fatalf("draft was edited to %q; a retry re-delivers, so no failure notice may outlive it", edit)
		}
	}
}

// TestDeliverStreamConfirmsShortReplyAsOneChunk covers the draft-as-final-reply
// path: the finalized draft is chunk 0 of 1, so a retry that sees cursor 1 has
// nothing left to send.
func TestDeliverStreamConfirmsShortReplyAsOneChunk(t *testing.T) {
	b, rest := newPublishingBot(t)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "short answer"}
	close(events)
	var confirmed []int64
	resume := textDelivery{confirm: func(_ context.Context, n int64) error {
		confirmed = append(confirmed, n)
		return nil
	}}
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: events}, nil, resume); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(confirmed, []int64{1}) {
		t.Fatalf("confirmed cursors = %v, want [1]", confirmed)
	}
	if got := rest.edited; len(got) != 1 || got[0] != "short answer" {
		t.Fatalf("edits = %#v, want the draft finalized into the reply", got)
	}
}

// TestDeliverStreamConfirmFailureFailsDeliveryAndDeletesDraft: losing the row
// means another attempt owns this response. Reporting success would strand the
// cursor behind what is on screen, so the delivery fails and takes the draft
// with it — the retry re-delivers the text as a normal message.
func TestDeliverStreamConfirmFailureFailsDeliveryAndDeletesDraft(t *testing.T) {
	b, rest := newPublishingBot(t)
	events := make(chan channel.Event, 1)
	events <- channel.Event{Text: "short answer"}
	close(events)
	lost := errors.New("dispatch ownership lost")
	resume := textDelivery{confirm: func(context.Context, int64) error { return lost }}
	err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: events}, nil, resume)
	if !errors.Is(err, lost) {
		t.Fatalf("deliverStream error = %v, want the confirmation failure", err)
	}
	rest.mu.Lock()
	defer rest.mu.Unlock()
	if !reflect.DeepEqual(rest.deletedMessage, []string{"channel:sent-message"}) {
		t.Fatalf("deleted drafts = %#v, want the unconfirmed draft removed", rest.deletedMessage)
	}
}

// TestDeliverStreamMediaFailureNeverFailsDelivery: attachments are not
// persisted, so failing here would requeue a dispatch whose only recovery is
// re-running the agent. The group is told an upload failed; the turn is done.
func TestDeliverStreamMediaFailureNeverFailsDelivery(t *testing.T) {
	b, rest := newPublishingBot(t)
	rest.sendErr = func(_ int, m *discordgo.MessageSend) error {
		if len(m.Files) > 0 {
			return errors.New("upload rejected")
		}
		return nil
	}
	events := make(chan channel.Event, 2)
	events <- channel.Event{Text: "answer with a chart"}
	events <- channel.Event{Image: &channel.ImageEvent{MimeType: "image/png", Data: "aGVsbG8="}}
	close(events)
	var confirmed []int64
	resume := textDelivery{confirm: func(_ context.Context, n int64) error {
		confirmed = append(confirmed, n)
		return nil
	}}
	if err := b.deliverStream(context.Background(), "channel", "request", &channel.ChatStream{Events: events}, nil, resume); err != nil {
		t.Fatalf("media failure returned %v, want the text delivery to stand", err)
	}
	if !reflect.DeepEqual(confirmed, []int64{1}) {
		t.Fatalf("confirmed cursors = %v, want the text chunk confirmed", confirmed)
	}
	sent := rest.sentContents()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "could not be uploaded") {
		t.Fatalf("sent = %#v, want a user-visible attachment failure notice", sent)
	}
}
