package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type mockChannel struct {
	name  string
	typ   string
	calls []pkgchannel.Notification
	err   error
}

func (m *mockChannel) Name() string                  { return m.name }
func (m *mockChannel) Platform() string              { return m.typ }
func (m *mockChannel) Start(_ context.Context) error { return nil }
func (m *mockChannel) Stop()                         {}
func (m *mockChannel) Notify(_ context.Context, n pkgchannel.Notification) error {
	m.calls = append(m.calls, n)
	return m.err
}

type mockChannelStore struct {
	channels []config.Channel
}

func (m mockChannelStore) ListChannels(context.Context) ([]config.Channel, error) {
	return m.channels, nil
}

func TestDispatcherBroadcast(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	slack := &mockChannel{name: "slack"}
	d.Register(tg)
	d.Register(slack)

	err := d.Notify(context.Background(), pkgchannel.Notification{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tg.calls) != 1 {
		t.Fatalf("telegram got %d calls, want 1", len(tg.calls))
	}
	if len(slack.calls) != 1 {
		t.Fatalf("slack got %d calls, want 1", len(slack.calls))
	}
}

func TestDispatcherAgentBoundBroadcastUsesDedicatedChannel(t *testing.T) {
	d := NewDispatcher()
	general := &mockChannel{name: "feishu", typ: "feishu"}
	dedicated := &mockChannel{name: "feishu-coder", typ: "feishu"}
	d.Register(general)
	d.Register(dedicated)
	d.SetChannelStore(mockChannelStore{channels: []config.Channel{
		{ID: "feishu", Type: "feishu", Enabled: true},
		{ID: "feishu-coder", Type: "feishu", AgentID: "coder", Enabled: true},
	}})

	err := d.Notify(context.Background(), pkgchannel.Notification{AgentID: "coder", Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dedicated.calls) != 1 {
		t.Fatalf("dedicated got %d calls, want 1", len(dedicated.calls))
	}
	if len(general.calls) != 0 {
		t.Fatalf("general got %d calls, want 0", len(general.calls))
	}
}

func TestDispatcherAgentBoundExplicitChannelTypeUsesDedicated(t *testing.T) {
	d := NewDispatcher()
	general := &mockChannel{name: "feishu", typ: "feishu"}
	dedicated := &mockChannel{name: "feishu-coder", typ: "feishu"}
	d.Register(general)
	d.Register(dedicated)
	d.SetChannelStore(mockChannelStore{channels: []config.Channel{
		{ID: "feishu", Type: "feishu", Enabled: true},
		{ID: "feishu-coder", Type: "feishu", AgentID: "coder", Enabled: true},
	}})

	// Agent "coder" sends with explicit channel="feishu" — should still
	// route to its dedicated channel, not the general feishu instance.
	err := d.Notify(context.Background(), pkgchannel.Notification{
		Channel: "feishu",
		ChatID:  "ou_abc",
		AgentID: "coder",
		Text:    "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dedicated.calls) != 1 {
		t.Fatalf("dedicated got %d calls, want 1", len(dedicated.calls))
	}
	if len(general.calls) != 0 {
		t.Fatalf("general got %d calls, want 0", len(general.calls))
	}
}

func TestDispatcherBroadcastSkipsDedicatedChannels(t *testing.T) {
	d := NewDispatcher()
	general := &mockChannel{name: "feishu", typ: "feishu"}
	dedicated := &mockChannel{name: "feishu-coder", typ: "feishu"}
	d.Register(general)
	d.Register(dedicated)
	d.SetChannelStore(mockChannelStore{channels: []config.Channel{
		{ID: "feishu", Type: "feishu", Enabled: true},
		{ID: "feishu-coder", Type: "feishu", AgentID: "coder", Enabled: true},
	}})

	err := d.Notify(context.Background(), pkgchannel.Notification{AgentID: "other", Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(general.calls) != 1 {
		t.Fatalf("general got %d calls, want 1", len(general.calls))
	}
	if len(dedicated.calls) != 0 {
		t.Fatalf("dedicated got %d calls, want 0", len(dedicated.calls))
	}
}

func TestDispatcherRouteToSpecific(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	slack := &mockChannel{name: "slack"}
	d.Register(tg)
	d.Register(slack)

	err := d.Notify(context.Background(), pkgchannel.Notification{
		Channel: "slack",
		Text:    "only slack",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tg.calls) != 0 {
		t.Errorf("telegram got %d calls, want 0", len(tg.calls))
	}
	if len(slack.calls) != 1 {
		t.Fatalf("slack got %d calls, want 1", len(slack.calls))
	}
}

func TestDispatcherExplicitChatID(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg)

	err := d.Notify(context.Background(), pkgchannel.Notification{
		Channel: "telegram",
		ChatID:  "override-chat",
		Text:    "test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tg.calls[0].ChatID != "override-chat" {
		t.Errorf("ChatID = %q, want %q", tg.calls[0].ChatID, "override-chat")
	}
}

func TestDispatcherUnknownChannel(t *testing.T) {
	d := NewDispatcher()
	d.Register(&mockChannel{name: "telegram"})

	err := d.Notify(context.Background(), pkgchannel.Notification{Channel: "discord", Text: "test"})
	if err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func TestDispatcherNoChannels(t *testing.T) {
	d := NewDispatcher()
	err := d.Notify(context.Background(), pkgchannel.Notification{Text: "test"})
	if err == nil {
		t.Fatal("expected error with no channels")
	}
}

func TestDispatcherPartialFailure(t *testing.T) {
	d := NewDispatcher()
	good := &mockChannel{name: "telegram"}
	bad := &mockChannel{name: "slack", err: errors.New("slack down")}
	d.Register(good)
	d.Register(bad)

	err := d.Notify(context.Background(), pkgchannel.Notification{Text: "test"})
	if err == nil {
		t.Fatal("expected error on partial failure")
	}
	if len(good.calls) != 1 {
		t.Errorf("good channel got %d calls, want 1", len(good.calls))
	}
}

func TestDispatcherChannels(t *testing.T) {
	d := NewDispatcher()
	d.Register(&mockChannel{name: "telegram"})
	d.Register(&mockChannel{name: "slack"})

	names := d.Channels()
	if len(names) != 2 {
		t.Fatalf("len(Channels()) = %d, want 2", len(names))
	}
	if names[0] != "telegram" || names[1] != "slack" {
		t.Errorf("Channels() = %v, want [telegram slack]", names)
	}
}

type mockAuthService struct {
	user       pkgplugins.UserInfo
	identities []pkgplugins.LinkedIdentity
}

func (m *mockAuthService) GetUser(_ context.Context, _ string) (pkgplugins.UserInfo, error) {
	return m.user, nil
}

func (m *mockAuthService) ListUserIdentities(_ context.Context, _ string) ([]pkgplugins.LinkedIdentity, error) {
	return m.identities, nil
}

func (*mockAuthService) GetIdentityByPlatform(_ context.Context, _, _ string) (pkgplugins.LinkedIdentity, error) {
	return pkgplugins.LinkedIdentity{}, errors.New("not implemented")
}

func TestNotifyUserSendsToFirstLinked(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	fs := &mockChannel{name: "feishu"}
	d.Register(tg)
	d.Register(fs)
	d.SetAuthService(&mockAuthService{
		user: pkgplugins.UserInfo{ID: "1"},
		identities: []pkgplugins.LinkedIdentity{
			{ID: "10", Platform: "telegram", ExternalID: "tg-123", LinkedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: "11", Platform: "feishu", ExternalID: "fs-456", LinkedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
	})

	err := d.NotifyUser(context.Background(), "1", pkgchannel.Notification{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tg.calls) != 1 {
		t.Errorf("telegram got %d calls, want 1", len(tg.calls))
	}
	if tg.calls[0].ChatID != "tg-123" {
		t.Errorf("telegram ChatID = %q, want %q", tg.calls[0].ChatID, "tg-123")
	}
	if tg.calls[0].RecipientID != "tg-123" {
		t.Errorf("telegram RecipientID = %q, want %q", tg.calls[0].RecipientID, "tg-123")
	}
	if len(fs.calls) != 0 {
		t.Errorf("feishu got %d calls, want 0", len(fs.calls))
	}
}

func TestNotifyUserSendsToPreferred(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	fs := &mockChannel{name: "feishu"}
	d.Register(tg)
	d.Register(fs)

	preferredID := "11"
	d.SetAuthService(&mockAuthService{
		user: pkgplugins.UserInfo{ID: "1", NotifyIdentityID: &preferredID},
		identities: []pkgplugins.LinkedIdentity{
			{ID: "10", Platform: "telegram", ExternalID: "tg-123", LinkedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: "11", Platform: "feishu", ExternalID: "fs-456", LinkedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
	})

	err := d.NotifyUser(context.Background(), "1", pkgchannel.Notification{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fs.calls) != 1 {
		t.Errorf("feishu got %d calls, want 1", len(fs.calls))
	}
	if fs.calls[0].ChatID != "fs-456" {
		t.Errorf("feishu ChatID = %q, want %q", fs.calls[0].ChatID, "fs-456")
	}
	if len(tg.calls) != 0 {
		t.Errorf("telegram got %d calls, want 0", len(tg.calls))
	}
}

func TestNotifyUserPreferredNotFoundFallsToFirst(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg)

	staleID := "999"
	d.SetAuthService(&mockAuthService{
		user: pkgplugins.UserInfo{ID: "1", NotifyIdentityID: &staleID},
		identities: []pkgplugins.LinkedIdentity{
			{ID: "10", Platform: "telegram", ExternalID: "tg-123"},
		},
	})

	err := d.NotifyUser(context.Background(), "1", pkgchannel.Notification{Text: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tg.calls) != 1 {
		t.Fatalf("telegram got %d calls, want 1", len(tg.calls))
	}
	if tg.calls[0].ChatID != "tg-123" {
		t.Errorf("ChatID = %q, want %q", tg.calls[0].ChatID, "tg-123")
	}
}

func TestNotifyUserAgentBoundUsesDedicatedChannel(t *testing.T) {
	d := NewDispatcher()
	general := &mockChannel{name: "feishu", typ: "feishu"}
	dedicated := &mockChannel{name: "feishu-coder", typ: "feishu"}
	d.Register(general)
	d.Register(dedicated)
	d.SetChannelStore(mockChannelStore{channels: []config.Channel{
		{ID: "feishu", Type: "feishu", Enabled: true},
		{ID: "feishu-coder", Type: "feishu", AgentID: "coder", Enabled: true},
	}})
	d.SetAuthService(&mockAuthService{
		user: pkgplugins.UserInfo{ID: "1"},
		identities: []pkgplugins.LinkedIdentity{
			{ID: "10", Platform: "feishu", ExternalID: "fs-123"},
		},
	})

	err := d.NotifyUser(context.Background(), "1", pkgchannel.Notification{AgentID: "coder", Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dedicated.calls) != 1 {
		t.Fatalf("dedicated got %d calls, want 1", len(dedicated.calls))
	}
	if dedicated.calls[0].ChatID != "fs-123" {
		t.Fatalf("dedicated ChatID = %q, want fs-123", dedicated.calls[0].ChatID)
	}
	if len(general.calls) != 0 {
		t.Fatalf("general got %d calls, want 0", len(general.calls))
	}
}

func TestNotifyUserAgentBoundDedicatedChannelTypeIsInstanceID(t *testing.T) {
	d := NewDispatcher()
	general := &mockChannel{name: "feishu", typ: "feishu"}
	dedicated := &mockChannel{name: "feishu-coder", typ: "feishu"}
	d.Register(general)
	d.Register(dedicated)
	d.SetChannelStore(mockChannelStore{channels: []config.Channel{
		{ID: "feishu", Type: "feishu", Enabled: true},
		{ID: "feishu-coder", Type: "feishu-coder", AgentID: "coder", Enabled: true},
	}})
	d.SetAuthService(&mockAuthService{
		user: pkgplugins.UserInfo{ID: "1"},
		identities: []pkgplugins.LinkedIdentity{
			{ID: "10", Platform: "feishu", ExternalID: "fs-123"},
		},
	})

	err := d.NotifyUser(context.Background(), "1", pkgchannel.Notification{AgentID: "coder", Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dedicated.calls) != 1 {
		t.Fatalf("dedicated got %d calls, want 1", len(dedicated.calls))
	}
	if dedicated.calls[0].ChatID != "fs-123" {
		t.Fatalf("dedicated ChatID = %q, want fs-123", dedicated.calls[0].ChatID)
	}
	if len(general.calls) != 0 {
		t.Fatalf("general got %d calls, want 0", len(general.calls))
	}
}

func TestNotifyUserOtherAgentUsesPlatformChannel(t *testing.T) {
	d := NewDispatcher()
	general := &mockChannel{name: "feishu", typ: "feishu"}
	dedicated := &mockChannel{name: "feishu-coder", typ: "feishu"}
	d.Register(general)
	d.Register(dedicated)
	d.SetChannelStore(mockChannelStore{channels: []config.Channel{
		{ID: "feishu", Type: "feishu", Enabled: true},
		{ID: "feishu-coder", Type: "feishu", AgentID: "coder", Enabled: true},
	}})
	d.SetAuthService(&mockAuthService{
		user: pkgplugins.UserInfo{ID: "1"},
		identities: []pkgplugins.LinkedIdentity{
			{ID: "10", Platform: "feishu", ExternalID: "fs-123"},
		},
	})

	err := d.NotifyUser(context.Background(), "1", pkgchannel.Notification{AgentID: "writer", Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(general.calls) != 1 {
		t.Fatalf("general got %d calls, want 1", len(general.calls))
	}
	if general.calls[0].ChatID != "fs-123" {
		t.Fatalf("general ChatID = %q, want fs-123", general.calls[0].ChatID)
	}
	if len(dedicated.calls) != 0 {
		t.Fatalf("dedicated got %d calls, want 0", len(dedicated.calls))
	}
}
