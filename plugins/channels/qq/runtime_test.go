package qq

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type fakeQQChannel struct {
	started chan struct{}
	stopped chan struct{}
}

func newFakeQQChannel() *fakeQQChannel {
	return &fakeQQChannel{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeQQChannel) Name() string { return pkgchannel.PlatformQQ }
func (c *fakeQQChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}
func (c *fakeQQChannel) Stop() {}
func (c *fakeQQChannel) Notify(ctx context.Context, n pkgchannel.Notification) error {
	return nil
}

type fakeNotificationRegistry struct {
	names map[string]struct{}
}

func newFakeNotificationRegistry() *fakeNotificationRegistry {
	return &fakeNotificationRegistry{names: map[string]struct{}{}}
}

func (r *fakeNotificationRegistry) Register(ch pkgchannel.Channel) {
	r.names[ch.Name()] = struct{}{}
}

func (r *fakeNotificationRegistry) Unregister(name string) {
	delete(r.names, name)
}

func (r *fakeNotificationRegistry) Channels() []string {
	out := make([]string, 0, len(r.names))
	for name := range r.names {
		out = append(out, name)
	}
	return out
}

func TestQQManagedRuntimeApplyDisableReconfigure(t *testing.T) {
	now := time.Date(2026, 4, 7, 14, 0, 0, 0, time.UTC)
	notifier := newFakeNotificationRegistry()
	first := newFakeQQChannel()
	second := newFakeQQChannel()
	built := 0
	runtime := NewQQManagedRuntime(QQRuntimeDeps{
		Parent:        context.Background(),
		Handler:       fakeChannelHandler{},
		Notifications: notifier,
		Now:           func() time.Time { return now },
		NewChannel: func(cfg QQConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			built++
			if built == 1 {
				return first, nil
			}
			return second, nil
		},
	})

	state := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "qq-app", "app_secret": "qq-secret"}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, first.started, "first qq start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformQQ {
		t.Fatalf("notifier channels after first apply = %v", got)
	}

	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after start: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", snap.State)
	}
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "qq-app-2", "app_secret": "qq-secret-2"}}); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, first.stopped, "first qq stop")
	waitClosed(t, second.started, "second qq start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformQQ {
		t.Fatalf("notifier channels after reconfigure = %v, want [qq]", got)
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: false, Config: map[string]any{"app_id": "qq-app-2", "app_secret": "qq-secret-2"}}); err != nil {
		t.Fatalf("disable apply: %v", err)
	}
	waitClosed(t, second.stopped, "second qq stop")
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

func TestQQManagedRuntimeMissingCredentialsMarksError(t *testing.T) {
	runtime := NewQQManagedRuntime(QQRuntimeDeps{Parent: context.Background(), Handler: fakeChannelHandler{}})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "qq-app"}}); err != nil {
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

func TestQQManagedRuntimeBuildFailureReturnsError(t *testing.T) {
	runtime := NewQQManagedRuntime(QQRuntimeDeps{
		Parent:  context.Background(),
		Handler: fakeChannelHandler{},
		NewChannel: func(cfg QQConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return nil, errors.New("boom")
		},
	})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "qq-app", "app_secret": "qq-secret"}}); err == nil {
		t.Fatal("expected build error")
	}
}

func TestDecodeAndRedactQQPluginConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"app_id":        "qq-app",
		"app_secret":    "qq-secret",
		"enable_notify": true,
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.AppID != "qq-app" || cfg.AppSecret != "qq-secret" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}

	got := RedactConfig(map[string]any{
		"app_id":        "qq-app",
		"app_secret":    "qq-secret",
		"enable_notify": true,
	})
	if got["app_secret"] != "***" {
		t.Fatalf("redacted app_secret = %#v, want %q", got["app_secret"], "***")
	}
	if got["app_id"] != "qq-app" {
		t.Fatalf("app_id = %#v, want %q", got["app_id"], "qq-app")
	}
}

type fakeChannelHandler struct{}

func (fakeChannelHandler) HandleIncoming(context.Context, pkgchannel.IncomingMessage, string, string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}

func (fakeChannelHandler) ListModels() []pkgchannel.ModelOption { return nil }

func (fakeChannelHandler) SwitchModel(string, string) error { return nil }

func (fakeChannelHandler) ListAgents(context.Context, pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}

func (fakeChannelHandler) SwitchAgent(context.Context, pkgchannel.IncomingMessage, string) error {
	return nil
}

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}
