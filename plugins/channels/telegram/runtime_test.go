package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	internalnotify "github.com/CherryHQ/stella/internal/notify"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type fakeTelegramChannel struct {
	started chan struct{}
	stopped chan struct{}
}

func newFakeTelegramChannel() *fakeTelegramChannel {
	return &fakeTelegramChannel{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeTelegramChannel) Name() string { return pkgchannel.PlatformTelegram }
func (c *fakeTelegramChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}
func (*fakeTelegramChannel) Stop()                                                 {}
func (*fakeTelegramChannel) Notify(context.Context, pkgchannel.Notification) error { return nil }

func TestManagedRuntimeApplyDisableReconfigure(t *testing.T) {
	now := time.Date(2026, 4, 7, 13, 0, 0, 0, time.UTC)
	notifier := internalnotify.NewDispatcher()
	first := newFakeTelegramChannel()
	second := newFakeTelegramChannel()
	built := 0
	runtime := NewManagedRuntime(RuntimeDeps{
		Parent:        context.Background(),
		Handler:       fakeChannelHandler{},
		Notifications: notifier,
		Now:           func() time.Time { return now },
		NewChannel: func(cfg TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			built++
			if built == 1 {
				return first, nil
			}
			return second, nil
		},
	})

	state := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"token": "tg-token"}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, first.started, "first start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformTelegram {
		t.Fatalf("notifier channels after first apply = %v", got)
	}

	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after start: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", snap.State)
	}
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"token": "tg-token-2", "channel_id": "@stella"}}); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, first.stopped, "first stop")
	waitClosed(t, second.started, "second start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformTelegram {
		t.Fatalf("notifier channels after reconfigure = %v, want [telegram]", got)
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: false, Config: map[string]any{"token": "tg-token-2"}}); err != nil {
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

func TestManagedRuntimeMissingTokenMarksError(t *testing.T) {
	runtime := NewManagedRuntime(RuntimeDeps{
		Parent:  context.Background(),
		Handler: fakeChannelHandler{},
		NewChannel: func(cfg TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return newFakeTelegramChannel(), nil
		},
	})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{}}); err != nil {
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

func TestManagedRuntimeBuildFailureReturnsError(t *testing.T) {
	runtime := NewManagedRuntime(RuntimeDeps{
		Parent:  context.Background(),
		Handler: fakeChannelHandler{},
		NewChannel: func(cfg TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return nil, errors.New("boom")
		},
	})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"token": "tg-token"}}); err == nil {
		t.Fatal("expected build error")
	}
}
