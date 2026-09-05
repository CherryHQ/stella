package weixin

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type fakeWeixinChannel struct {
	started chan struct{}
	stopped chan struct{}
}

func newFakeWeixinChannel() *fakeWeixinChannel {
	return &fakeWeixinChannel{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeWeixinChannel) Name() string { return pkgchannel.PlatformWeixin }
func (c *fakeWeixinChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}
func (c *fakeWeixinChannel) Stop() {}
func (c *fakeWeixinChannel) Notify(ctx context.Context, n pkgchannel.Notification) error {
	return nil
}

func TestWeixinManagedRuntimeApplyDisableReconfigure(t *testing.T) {
	now := time.Date(2026, 4, 7, 16, 0, 0, 0, time.UTC)
	notifier := newFakeNotificationRegistry()
	first := newFakeWeixinChannel()
	second := newFakeWeixinChannel()
	built := 0
	runtime := NewWeixinManagedRuntime(WeixinRuntimeDeps{
		Parent:        context.Background(),
		Handler:       fakeChannelHandler{},
		Notifications: notifier,
		Now:           func() time.Time { return now },
		NewChannel: func(cfg WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			built++
			if built == 1 {
				return first, nil
			}
			return second, nil
		},
	})

	state := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"bot_token": "wx-token", "base_url": "https://wx.example", "bot_id": "bot-1", "user_id": "user-1"}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, first.started, "first weixin start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformWeixin {
		t.Fatalf("notifier channels after first apply = %v", got)
	}

	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after start: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", snap.State)
	}
	if snap.Metadata["has_bot_identity"] != true {
		t.Fatalf("has_bot_identity = %#v, want true", snap.Metadata["has_bot_identity"])
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"bot_token": "wx-token-2"}}); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, first.stopped, "first weixin stop")
	waitClosed(t, second.started, "second weixin start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformWeixin {
		t.Fatalf("notifier channels after reconfigure = %v, want [weixin]", got)
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: false, Config: map[string]any{"bot_token": "wx-token-2"}}); err != nil {
		t.Fatalf("disable apply: %v", err)
	}
	waitClosed(t, second.stopped, "second weixin stop")
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

func TestWeixinManagedRuntimeMissingTokenMarksError(t *testing.T) {
	runtime := NewWeixinManagedRuntime(WeixinRuntimeDeps{Parent: context.Background(), Handler: fakeChannelHandler{}})
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

func TestWeixinManagedRuntimeBuildFailureReturnsError(t *testing.T) {
	runtime := NewWeixinManagedRuntime(WeixinRuntimeDeps{
		Parent:  context.Background(),
		Handler: fakeChannelHandler{},
		NewChannel: func(cfg WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return nil, errors.New("boom")
		},
	})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"bot_token": "wx-token"}}); err == nil {
		t.Fatal("expected build error")
	}
}

func TestDecodeAndRedactWeixinPluginConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"bot_token": "wx-token", "base_url": "https://wx.example", "bot_id": "bot-1", "user_id": "user-1", "enable_notify": true})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.BotToken != "wx-token" || cfg.BaseURL != "https://wx.example" || cfg.BotID != "bot-1" || cfg.UserID != "user-1" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}

	got := RedactConfig(map[string]any{"bot_token": "wx-token", "base_url": "https://wx.example"})
	if got["bot_token"] != "***" {
		t.Fatalf("redacted bot_token = %#v, want %q", got["bot_token"], "***")
	}
	if got["base_url"] != "https://wx.example" {
		t.Fatalf("base_url = %#v, want %q", got["base_url"], "https://wx.example")
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

type fakeNotificationRegistry struct {
	names   map[string]struct{}
	channel pkgchannel.Channel
}

func newFakeNotificationRegistry() *fakeNotificationRegistry {
	return &fakeNotificationRegistry{names: map[string]struct{}{}}
}

func (r *fakeNotificationRegistry) Register(ch pkgchannel.Channel) {
	r.channel = ch
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

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}
