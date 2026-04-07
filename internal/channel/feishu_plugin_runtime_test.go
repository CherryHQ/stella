package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type fakeFeishuChannel struct {
	started chan struct{}
	stopped chan struct{}
}

func newFakeFeishuChannel() *fakeFeishuChannel {
	return &fakeFeishuChannel{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeFeishuChannel) Name() string { return PlatformFeishu }
func (c *fakeFeishuChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return ctx.Err()
}
func (c *fakeFeishuChannel) Stop() {}
func (c *fakeFeishuChannel) Notify(ctx context.Context, n pkgchannel.Notification) error {
	return nil
}

func TestFeishuManagedRuntimeApplyDisableReconfigure(t *testing.T) {
	now := time.Date(2026, 4, 7, 15, 0, 0, 0, time.UTC)
	notifier := NewDispatcher()
	first := newFakeFeishuChannel()
	second := newFakeFeishuChannel()
	built := 0
	runtime := NewFeishuManagedRuntime(FeishuRuntimeDeps{
		Parent:   context.Background(),
		Handler:  fakeChannelHandler{},
		Notifier: notifier,
		Now:      func() time.Time { return now },
		NewChannel: func(cfg FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			built++
			if built == 1 {
				return first, nil
			}
			return second, nil
		},
	})

	state := pkgplugins.PluginState{ID: FeishuPluginID, Enabled: true, Config: map[string]any{
		"app_id":             "fs-app",
		"app_secret":         "fs-secret",
		"encrypt_key":        "enc",
		"verification_token": "verify",
		"enable_notify":      true,
		"group_mode":         "mention",
		"groups": map[string]any{
			"chat-1": map[string]any{"group_mode": "always", "system_prompt": "be brief", "tool_allow": []any{"shell"}},
		},
	}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, first.started, "first feishu start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != PlatformFeishu {
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
	if snap.Metadata["group_count"] != 1 {
		t.Fatalf("group_count = %#v, want 1", snap.Metadata["group_count"])
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: FeishuPluginID, Enabled: true, Config: map[string]any{"app_id": "fs-app-2", "app_secret": "fs-secret-2"}}); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, first.stopped, "first feishu stop")
	waitClosed(t, second.started, "second feishu start")
	if got := notifier.Channels(); len(got) != 0 {
		t.Fatalf("notifier channels after reconfigure = %v, want []", got)
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: FeishuPluginID, Enabled: false, Config: map[string]any{"app_id": "fs-app-2", "app_secret": "fs-secret-2"}}); err != nil {
		t.Fatalf("disable apply: %v", err)
	}
	waitClosed(t, second.stopped, "second feishu stop")
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

func TestFeishuManagedRuntimeMissingCredentialsMarksError(t *testing.T) {
	runtime := NewFeishuManagedRuntime(FeishuRuntimeDeps{Parent: context.Background(), Handler: fakeChannelHandler{}})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: FeishuPluginID, Enabled: true, Config: map[string]any{"app_id": "fs-app"}}); err != nil {
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

func TestFeishuManagedRuntimeBuildFailureReturnsError(t *testing.T) {
	runtime := NewFeishuManagedRuntime(FeishuRuntimeDeps{
		Parent:  context.Background(),
		Handler: fakeChannelHandler{},
		NewChannel: func(cfg FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return nil, errors.New("boom")
		},
	})
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: FeishuPluginID, Enabled: true, Config: map[string]any{"app_id": "fs-app", "app_secret": "fs-secret"}}); err == nil {
		t.Fatal("expected build error")
	}
}

func TestDecodeAndRedactFeishuPluginConfig(t *testing.T) {
	cfg, err := DecodeFeishuPluginConfig(map[string]any{
		"app_id":             "fs-app",
		"app_secret":         "fs-secret",
		"encrypt_key":        "enc",
		"verification_token": "verify",
		"group_mode":         "mention",
		"enable_notify":      true,
		"groups": map[string]any{
			"chat-1": map[string]any{"group_mode": "always", "system_prompt": "be brief", "tool_allow": []any{"shell"}, "tool_deny": []any{"danger"}},
		},
	})
	if err != nil {
		t.Fatalf("DecodeFeishuPluginConfig: %v", err)
	}
	if cfg.AppID != "fs-app" || cfg.AppSecret != "fs-secret" || cfg.EncryptKey != "enc" || cfg.VerificationToken != "verify" || cfg.GroupMode != "mention" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}
	if len(cfg.Groups) != 1 || cfg.Groups["chat-1"].SystemPrompt != "be brief" {
		t.Fatalf("decoded groups = %#v", cfg.Groups)
	}

	got := RedactFeishuPluginConfig(map[string]any{
		"app_id":             "fs-app",
		"app_secret":         "fs-secret",
		"encrypt_key":        "enc",
		"verification_token": "verify",
		"enable_notify":      true,
	})
	if got["app_secret"] != "***" {
		t.Fatalf("redacted app_secret = %#v, want %q", got["app_secret"], "***")
	}
	if got["encrypt_key"] != "***" {
		t.Fatalf("redacted encrypt_key = %#v, want %q", got["encrypt_key"], "***")
	}
	if got["verification_token"] != "***" {
		t.Fatalf("redacted verification_token = %#v, want %q", got["verification_token"], "***")
	}
	if got["app_id"] != "fs-app" {
		t.Fatalf("app_id = %#v, want %q", got["app_id"], "fs-app")
	}
}
