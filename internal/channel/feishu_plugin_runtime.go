package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	feishuplugin "github.com/vaayne/anna/plugins/channels/feishu"
)

const (
	FeishuPluginID    = "channel/feishu"
	FeishuRuntimeName = "bot"
)

type FeishuRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *Dispatcher
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(FeishuConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

type feishuManagedRuntime struct {
	deps FeishuRuntimeDeps

	mu         sync.RWMutex
	cancel     context.CancelFunc
	generation int
	snapshot   pkgplugins.RuntimeSnapshot
}

func NewFeishuManagedRuntime(deps FeishuRuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.Parent == nil {
		deps.Parent = context.Background()
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.NewChannel == nil {
		deps.NewChannel = func(cfg FeishuConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return feishuplugin.New(feishuplugin.Config{
				AppID:             cfg.AppID,
				AppSecret:         cfg.AppSecret,
				EncryptKey:        cfg.EncryptKey,
				VerificationToken: cfg.VerificationToken,
				GroupMode:         cfg.GroupMode,
				Groups:            feishuGroupsToPluginConfig(cfg.Groups),
			}, handler)
		}
	}
	return &feishuManagedRuntime{
		deps: deps,
		snapshot: pkgplugins.RuntimeSnapshot{
			State:     pkgplugins.RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *feishuManagedRuntime) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	cfg, err := DecodeFeishuPluginConfig(desired.Config)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.generation++
	generation := r.generation
	cancel := r.cancel
	r.cancel = nil
	if cancel != nil {
		cancel()
	}
	if r.deps.Notifier != nil {
		r.deps.Notifier.Unregister(PlatformFeishu)
	}
	if !desired.Enabled {
		r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "feishu disabled", cfg)
		r.mu.Unlock()
		return nil
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, "feishu: missing app_id or app_secret", cfg)
		r.mu.Unlock()
		return nil
	}

	ch, err := r.deps.NewChannel(cfg, r.deps.Handler)
	if err != nil {
		r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, err.Error(), cfg)
		r.mu.Unlock()
		return fmt.Errorf("build feishu channel: %w", err)
	}
	if ch.Name() != PlatformFeishu {
		r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, "feishu: unexpected channel name", cfg)
		r.mu.Unlock()
		return fmt.Errorf("build feishu channel: unexpected channel name %q", ch.Name())
	}
	if cfg.EnableNotify && r.deps.Notifier != nil {
		r.deps.Notifier.Register(ch)
	}
	rctx, stop := context.WithCancel(r.deps.Parent)
	r.cancel = stop
	r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateRunning, "feishu running", cfg)
	r.mu.Unlock()

	go func() {
		err := ch.Start(rctx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		if r.deps.Notifier != nil {
			r.deps.Notifier.Unregister(PlatformFeishu)
		}
		r.cancel = nil
		message := "feishu stopped"
		state := pkgplugins.RuntimeStateStopped
		if err != nil && rctx.Err() == nil {
			message = err.Error()
			state = pkgplugins.RuntimeStateError
			r.deps.Log.Error("feishu: stopped unexpectedly", "error", err)
		}
		r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), state, message, cfg)
	}()

	return nil
}

func (r *feishuManagedRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.deps.Notifier != nil {
		r.deps.Notifier.Unregister(PlatformFeishu)
	}
	r.snapshot = feishuRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "feishu stopped", FeishuConfig{})
	return nil
}

func (r *feishuManagedRuntime) Snapshot(ctx context.Context) (pkgplugins.RuntimeSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}

func DecodeFeishuPluginConfig(raw map[string]any) (FeishuConfig, error) {
	if raw == nil {
		return FeishuConfig{}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return FeishuConfig{}, fmt.Errorf("marshal feishu config: %w", err)
	}
	var cfg FeishuConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return FeishuConfig{}, fmt.Errorf("decode feishu config: %w", err)
	}
	return cfg, nil
}

func RedactFeishuPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeFeishuPluginConfig(raw)
	if err != nil {
		return cloneFeishuConfigMap(raw)
	}
	out := cloneFeishuConfigMap(raw)
	if cfg.AppSecret != "" {
		out["app_secret"] = "***"
	}
	if cfg.EncryptKey != "" {
		out["encrypt_key"] = "***"
	}
	if cfg.VerificationToken != "" {
		out["verification_token"] = "***"
	}
	return out
}

func feishuRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg FeishuConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"app_id":                 cfg.AppID,
			"group_mode":             cfg.GroupMode,
			"notify_enabled":         cfg.EnableNotify,
			"group_count":            len(cfg.Groups),
			"has_encrypt_key":        cfg.EncryptKey != "",
			"has_verification_token": cfg.VerificationToken != "",
		},
	}
}

func cloneFeishuConfigMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func feishuGroupsToPluginConfig(groups map[string]FeishuGroup) map[string]feishuplugin.GroupConfig {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]feishuplugin.GroupConfig, len(groups))
	for k, v := range groups {
		out[k] = feishuplugin.GroupConfig{
			GroupMode:    v.GroupMode,
			SystemPrompt: v.SystemPrompt,
			ToolAllow:    append([]string(nil), v.ToolAllow...),
			ToolDeny:     append([]string(nil), v.ToolDeny...),
		}
	}
	return out
}
