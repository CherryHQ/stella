package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

type mockChannel struct {
	name  string
	calls []pkgchannel.Notification
	err   error
}

func (m *mockChannel) Name() string                  { return m.name }
func (m *mockChannel) Start(_ context.Context) error { return nil }
func (m *mockChannel) Stop()                         {}
func (m *mockChannel) Notify(_ context.Context, n pkgchannel.Notification) error {
	m.calls = append(m.calls, n)
	return m.err
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

func TestToolDefinition(t *testing.T) {
	d := NewDispatcher()
	tool := NewTool(d)

	def := tool.Definition()
	if def.Name != "notify" {
		t.Errorf("Name = %q, want %q", def.Name, "notify")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestToolExecuteBroadcast(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg)

	tool := NewTool(d)
	result, err := tool.Execute(context.Background(), map[string]any{
		"message": "hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tg.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(tg.calls))
	}
	if tg.calls[0].Text != "hello world" {
		t.Errorf("Text = %q", tg.calls[0].Text)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestToolExecuteTargeted(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg)

	tool := NewTool(d)
	result, err := tool.Execute(context.Background(), map[string]any{
		"message": "targeted",
		"channel": "telegram",
		"chat_id": "custom-chat",
		"silent":  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tg.calls[0].ChatID != "custom-chat" {
		t.Errorf("ChatID = %q, want %q", tg.calls[0].ChatID, "custom-chat")
	}
	if !tg.calls[0].Silent {
		t.Error("expected Silent to be true")
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestToolExecuteEmptyMessage(t *testing.T) {
	d := NewDispatcher()
	tool := NewTool(d)

	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestToolExecuteError(t *testing.T) {
	d := NewDispatcher()
	bad := &mockChannel{name: "telegram", err: errors.New("send failed")}
	d.Register(bad)

	tool := NewTool(d)
	_, err := tool.Execute(context.Background(), map[string]any{
		"message": "test",
		"channel": "telegram",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

type mockAuthStore struct {
	auth.AuthStore
	user       auth.AuthUser
	identities []auth.Identity
}

func (m *mockAuthStore) GetUser(_ context.Context, _ int64) (auth.AuthUser, error) {
	return m.user, nil
}

func (m *mockAuthStore) ListIdentitiesByUser(_ context.Context, _ int64) ([]auth.Identity, error) {
	return m.identities, nil
}

func TestNotifyUserSendsToFirstLinked(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	fs := &mockChannel{name: "feishu"}
	d.Register(tg)
	d.Register(fs)
	d.SetAuthStore(&mockAuthStore{
		user: auth.AuthUser{ID: 1},
		identities: []auth.Identity{
			{ID: 10, Platform: "telegram", ExternalID: "tg-123", LinkedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: 11, Platform: "feishu", ExternalID: "fs-456", LinkedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
	})

	err := d.NotifyUser(context.Background(), 1, pkgchannel.Notification{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tg.calls) != 1 {
		t.Errorf("telegram got %d calls, want 1", len(tg.calls))
	}
	if tg.calls[0].ChatID != "tg-123" {
		t.Errorf("telegram ChatID = %q, want %q", tg.calls[0].ChatID, "tg-123")
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

	preferredID := int64(11)
	d.SetAuthStore(&mockAuthStore{
		user: auth.AuthUser{ID: 1, NotifyIdentityID: &preferredID},
		identities: []auth.Identity{
			{ID: 10, Platform: "telegram", ExternalID: "tg-123", LinkedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: 11, Platform: "feishu", ExternalID: "fs-456", LinkedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
	})

	err := d.NotifyUser(context.Background(), 1, pkgchannel.Notification{Text: "hello"})
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

	staleID := int64(999)
	d.SetAuthStore(&mockAuthStore{
		user: auth.AuthUser{ID: 1, NotifyIdentityID: &staleID},
		identities: []auth.Identity{
			{ID: 10, Platform: "telegram", ExternalID: "tg-123"},
		},
	})

	err := d.NotifyUser(context.Background(), 1, pkgchannel.Notification{Text: "test"})
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
