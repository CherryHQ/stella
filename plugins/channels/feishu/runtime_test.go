package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type fakeFeishuChannel struct {
	started chan struct{}
	stopped chan struct{}
}

func newFakeFeishuChannel() *fakeFeishuChannel {
	return &fakeFeishuChannel{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeFeishuChannel) Name() string { return pkgchannel.PlatformFeishu }
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
	notifier := newFakeNotificationRegistry()
	first := newFakeFeishuChannel()
	second := newFakeFeishuChannel()
	built := 0
	runtime := NewFeishuManagedRuntime(FeishuRuntimeDeps{
		Parent:        context.Background(),
		Handler:       fakeChannelHandler{},
		Notifications: notifier,
		Now:           func() time.Time { return now },
		NewChannel: func(cfg FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			built++
			if built == 1 {
				return first, nil
			}
			return second, nil
		},
	})

	state := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{
		"app_id":             "fs-app",
		"app_secret":         "fs-secret",
		"encrypt_key":        "enc",
		"verification_token": "verify",
		"groups": map[string]any{
			"chat-1": map[string]any{"system_prompt": "be brief", "tool_allow": []any{"shell"}},
		},
	}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, first.started, "first feishu start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformFeishu {
		t.Fatalf("notifier channels after first apply = %v", got)
	}

	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after start: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state = %q, want running", snap.State)
	}
	if snap.Metadata["group_count"] != 1 {
		t.Fatalf("group_count = %#v, want 1", snap.Metadata["group_count"])
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "fs-app-2", "app_secret": "fs-secret-2"}}); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, first.stopped, "first feishu stop")
	waitClosed(t, second.started, "second feishu start")
	if got := notifier.Channels(); len(got) != 1 || got[0] != pkgchannel.PlatformFeishu {
		t.Fatalf("notifier channels after reconfigure = %v, want [feishu]", got)
	}

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: false, Config: map[string]any{"app_id": "fs-app-2", "app_secret": "fs-secret-2"}}); err != nil {
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
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "fs-app"}}); err != nil {
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

func TestFeishuEmptyConfigPassesPersistenceValidation(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{})
	if err != nil || validateConfigValues(cfg) != "" {
		t.Fatalf("empty disabled-channel config should pass persistence validation: cfg=%#v err=%v validation=%q", cfg, err, validateConfigValues(cfg))
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
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"app_id": "fs-app", "app_secret": "fs-secret"}}); err == nil {
		t.Fatal("expected build error")
	}
}

func TestDecodeAndRedactFeishuPluginConfig(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{
		"app_id":             "fs-app",
		"app_secret":         "fs-secret",
		"encrypt_key":        "enc",
		"verification_token": "verify",
		"enable_notify":      true,
		"groups": map[string]any{
			"chat-1": map[string]any{
				"system_prompt":    "be brief",
				"tool_allow":       []any{"shell"},
				"tool_deny":        []any{"danger"},
				"enabled":          true,
				"require_mention":  false,
				"allowed_users":    []any{"on_allowed"},
				"disallowed_users": []any{"on_denied"},
			},
		},
	})
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.AppID != "fs-app" || cfg.AppSecret != "fs-secret" || cfg.EncryptKey != "enc" || cfg.VerificationToken != "verify" || !cfg.EnableNotify {
		t.Fatalf("decoded config = %#v", cfg)
	}
	group := cfg.Groups["chat-1"]
	if len(cfg.Groups) != 1 || group.SystemPrompt != "be brief" || group.Enabled == nil || !*group.Enabled || group.RequireMention == nil || *group.RequireMention || len(group.AllowedUsers) != 1 || len(group.DisallowedUsers) != 1 {
		t.Fatalf("decoded groups = %#v", cfg.Groups)
	}

	got := RedactConfig(map[string]any{
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

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

func TestValidateConfigAutoProvisionNoTenantKey(t *testing.T) {
	// tenant_key is optional; auto-detected at startup via the Feishu tenant API.
	cfg, _ := DecodeConfig(map[string]any{"app_id": "a", "app_secret": "s", "auto_provision": true})
	if got := validateConfig(cfg); got != "" {
		t.Errorf("unexpected validation error: %q", got)
	}
}

func TestValidateConfigAutoProvisionWithTenantKey(t *testing.T) {
	cfg, _ := DecodeConfig(map[string]any{"app_id": "a", "app_secret": "s", "auto_provision": true, "tenant_key": "tenant123"})
	if got := validateConfig(cfg); got != "" {
		t.Errorf("unexpected validation error: %q", got)
	}
}

func TestValidateConfigNoAutoProvision(t *testing.T) {
	cfg, _ := DecodeConfig(map[string]any{"app_id": "a", "app_secret": "s"})
	if got := validateConfig(cfg); got != "" {
		t.Errorf("unexpected validation error: %q", got)
	}
}

func TestDecodeConfigAppliesAdmissionDefaults(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{"app_id": "a", "app_secret": "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowDM || cfg.AllowUnlinkedDM || !cfg.RequireMention || cfg.GuestMessageLimitPerMinute != pkgchannel.DefaultGuestMessageLimitPerMinute || cfg.GuestMaxPerChannel != pkgchannel.DefaultGuestMaxPerChannel || cfg.GuestRetentionDays != pkgchannel.DefaultGuestRetentionDays {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
	explicit, err := DecodeConfig(map[string]any{"app_id": "a", "app_secret": "s", "allow_dm": false, "require_mention": false})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.AllowDM || explicit.RequireMention {
		t.Fatalf("explicit false values lost: %#v", explicit)
	}
	properties := configSchema()["properties"].(map[string]any)
	for _, key := range []string{"allow_group", "allow_dm", "allow_unlinked_dm", "guest_message_limit_per_minute", "guest_max_per_channel", "guest_retention_days", "require_mention"} {
		if properties[key] == nil {
			t.Fatalf("schema missing %s", key)
		}
	}
}
