package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type fakeTelegramChannel struct {
	started chan struct{}
	stopped chan struct{}
}

func newFakeTelegramChannel() *fakeTelegramChannel {
	return &fakeTelegramChannel{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeTelegramChannel) Name() string { return PlatformTelegram }
func (c *fakeTelegramChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}
func (c *fakeTelegramChannel) Stop() {}
func (c *fakeTelegramChannel) Notify(ctx context.Context, n pkgchannel.Notification) error {
	return nil
}

type fakeChannelHandler struct{}

func (fakeChannelHandler) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (fakeChannelHandler) ListModels() []pkgchannel.ModelOption     { return nil }
func (fakeChannelHandler) SwitchModel(provider, model string) error { return nil }
func (fakeChannelHandler) ListAgents(ctx context.Context, msg pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}
func (fakeChannelHandler) SwitchAgent(ctx context.Context, msg pkgchannel.IncomingMessage, agentSlug string) error {
	return nil
}

func TestTelegramManagedRuntimeApplyDisableReconfigure(t *testing.T) {
	now := time.Date(2026, 4, 7, 13, 0, 0, 0, time.UTC)
	notifier := NewDispatcher()
	first := newFakeTelegramChannel()
	second := newFakeTelegramChannel()
	built := 0
	runtime := NewTelegramManagedRuntime(TelegramRuntimeDeps{
		Parent:   context.Background(),
		Handler:  fakeChannelHandler{},
		Notifier: notifier,
		Now:      func() time.Time { return now },
		NewChannel: func(cfg TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			built++
			if built == 1 {
				return first, nil
			}
			return second, nil
		},
	}).(*telegramManagedRuntime)

	state := pkgplugins.PluginState{ID: TelegramPluginID, Enabled: true, Config: map[string]any{"token": "tg-token", "enable_notify": true, "group_mode": "mention"}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, first.started, "first start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != PlatformTelegram {
		t.Fatalf("notifier channels after first apply = %v", got)
	}

	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after start: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", snap.State)
	}
	if snap.Metadata["notify_enabled"] != true {
		t.Fatalf("notify_enabled = %#v, want true", snap.Metadata["notify_enabled"])
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: TelegramPluginID, Enabled: true, Config: map[string]any{"token": "tg-token-2", "channel_id": "@anna"}}); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, first.stopped, "first stop")
	waitClosed(t, second.started, "second start")
	if got := notifier.Channels(); len(got) != 0 {
		t.Fatalf("notifier channels after reconfigure = %v, want []", got)
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: TelegramPluginID, Enabled: false, Config: map[string]any{"token": "tg-token-2"}}); err != nil {
		t.Fatalf("disable apply: %v", err)
	}
	waitClosed(t, second.stopped, "second stop")
	if got := notifier.Channels(); len(got) != 0 {
		t.Fatalf("notifier channels after disable = %v, want []", got)
	}

	snap, err = runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after disable: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateStopped {
		t.Fatalf("state after disable = %q, want stopped", snap.State)
	}
}

func TestTelegramManagedRuntimeMissingTokenMarksError(t *testing.T) {
	runtime := NewTelegramManagedRuntime(TelegramRuntimeDeps{Parent: context.Background(), Handler: fakeChannelHandler{}})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: TelegramPluginID, Enabled: true, Config: map[string]any{}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateError {
		t.Fatalf("state = %q, want error", snap.State)
	}
}

func TestTelegramManagedRuntimeBuildFailureReturnsError(t *testing.T) {
	runtime := NewTelegramManagedRuntime(TelegramRuntimeDeps{
		Parent:  context.Background(),
		Handler: fakeChannelHandler{},
		NewChannel: func(cfg TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return nil, errors.New("boom")
		},
	})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: TelegramPluginID, Enabled: true, Config: map[string]any{"token": "tg-token"}}); err == nil {
		t.Fatal("expected build error")
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}
