package channel

import (
	"context"
	"errors"
	"testing"
)

// mockChannel is a test Channel that records Notify calls.
type mockChannel struct {
	name  string
	calls []Notification
	err   error
}

func (m *mockChannel) Name() string                  { return m.name }
func (m *mockChannel) Start(_ context.Context) error { return nil }
func (m *mockChannel) Stop()                         {}
func (m *mockChannel) Notify(_ context.Context, n Notification) error {
	m.calls = append(m.calls, n)
	return m.err
}

func TestDispatcherBroadcast(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	slack := &mockChannel{name: "slack"}
	d.Register(tg, "tg-chat")
	d.Register(slack, "slack-channel")

	err := d.Notify(context.Background(), Notification{Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tg.calls) != 1 {
		t.Fatalf("telegram got %d calls, want 1", len(tg.calls))
	}
	if tg.calls[0].ChatID != "tg-chat" {
		t.Errorf("telegram ChatID = %q, want %q", tg.calls[0].ChatID, "tg-chat")
	}
	if len(slack.calls) != 1 {
		t.Fatalf("slack got %d calls, want 1", len(slack.calls))
	}
	if slack.calls[0].ChatID != "slack-channel" {
		t.Errorf("slack ChatID = %q, want %q", slack.calls[0].ChatID, "slack-channel")
	}
}

func TestDispatcherRouteToSpecific(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	slack := &mockChannel{name: "slack"}
	d.Register(tg, "tg-chat")
	d.Register(slack, "slack-channel")

	err := d.Notify(context.Background(), Notification{
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
	if slack.calls[0].ChatID != "slack-channel" {
		t.Errorf("slack ChatID = %q, want default %q", slack.calls[0].ChatID, "slack-channel")
	}
}

func TestDispatcherExplicitChatID(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg, "default-chat")

	err := d.Notify(context.Background(), Notification{
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
	d.Register(&mockChannel{name: "telegram"}, "chat")

	err := d.Notify(context.Background(), Notification{Channel: "discord", Text: "test"})
	if err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func TestDispatcherNoChannels(t *testing.T) {
	d := NewDispatcher()
	err := d.Notify(context.Background(), Notification{Text: "test"})
	if err == nil {
		t.Fatal("expected error with no channels")
	}
}

func TestDispatcherPartialFailure(t *testing.T) {
	d := NewDispatcher()
	good := &mockChannel{name: "telegram"}
	bad := &mockChannel{name: "slack", err: errors.New("slack down")}
	d.Register(good, "chat")
	d.Register(bad, "channel")

	err := d.Notify(context.Background(), Notification{Text: "test"})
	if err == nil {
		t.Fatal("expected error on partial failure")
	}
	// Good channel should still have been called.
	if len(good.calls) != 1 {
		t.Errorf("good channel got %d calls, want 1", len(good.calls))
	}
}

func TestDispatcherChannels(t *testing.T) {
	d := NewDispatcher()
	d.Register(&mockChannel{name: "telegram"}, "")
	d.Register(&mockChannel{name: "slack"}, "")

	names := d.Channels()
	if len(names) != 2 {
		t.Fatalf("len(Channels()) = %d, want 2", len(names))
	}
	if names[0] != "telegram" || names[1] != "slack" {
		t.Errorf("Channels() = %v, want [telegram slack]", names)
	}
}

func TestNotifyToolDefinition(t *testing.T) {
	d := NewDispatcher()
	tool := NewNotifyTool(d)

	def := tool.Definition()
	if def.Name != "notify" {
		t.Errorf("Name = %q, want %q", def.Name, "notify")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestNotifyToolExecuteBroadcast(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg, "chat-123")

	tool := NewNotifyTool(d)
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

func TestNotifyToolExecuteTargeted(t *testing.T) {
	d := NewDispatcher()
	tg := &mockChannel{name: "telegram"}
	d.Register(tg, "default-chat")

	tool := NewNotifyTool(d)
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

func TestNotifyToolExecuteEmptyMessage(t *testing.T) {
	d := NewDispatcher()
	tool := NewNotifyTool(d)

	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestNotifyToolExecuteError(t *testing.T) {
	d := NewDispatcher()
	bad := &mockChannel{name: "telegram", err: errors.New("send failed")}
	d.Register(bad, "chat")

	tool := NewNotifyTool(d)
	_, err := tool.Execute(context.Background(), map[string]any{
		"message": "test",
		"channel": "telegram",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
