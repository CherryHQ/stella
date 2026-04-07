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
	weixinplugin "github.com/vaayne/anna/plugins/channels/weixin"
)

const (
	WeixinPluginID    = "channel/weixin"
	WeixinRuntimeName = "bot"
)

type WeixinRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *Dispatcher
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(WeixinConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

type weixinManagedRuntime struct {
	deps WeixinRuntimeDeps

	mu         sync.RWMutex
	cancel     context.CancelFunc
	generation int
	snapshot   pkgplugins.RuntimeSnapshot
}

func NewWeixinManagedRuntime(deps WeixinRuntimeDeps) pkgplugins.ManagedRuntime {
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
		deps.NewChannel = func(cfg WeixinConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return weixinplugin.New(weixinplugin.Config{
				BotToken: cfg.BotToken,
				BaseURL:  cfg.BaseURL,
				BotID:    cfg.BotID,
				UserID:   cfg.UserID,
			}, handler)
		}
	}
	return &weixinManagedRuntime{
		deps: deps,
		snapshot: pkgplugins.RuntimeSnapshot{
			State:     pkgplugins.RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *weixinManagedRuntime) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	cfg, err := DecodeWeixinPluginConfig(desired.Config)
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
		r.deps.Notifier.Unregister(PlatformWeixin)
	}
	if !desired.Enabled {
		r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "weixin disabled", cfg)
		r.mu.Unlock()
		return nil
	}
	if cfg.BotToken == "" {
		r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, "weixin: missing bot_token", cfg)
		r.mu.Unlock()
		return nil
	}

	ch, err := r.deps.NewChannel(cfg, r.deps.Handler)
	if err != nil {
		r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, err.Error(), cfg)
		r.mu.Unlock()
		return fmt.Errorf("build weixin channel: %w", err)
	}
	if ch.Name() != PlatformWeixin {
		r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, "weixin: unexpected channel name", cfg)
		r.mu.Unlock()
		return fmt.Errorf("build weixin channel: unexpected channel name %q", ch.Name())
	}
	if cfg.EnableNotify && r.deps.Notifier != nil {
		r.deps.Notifier.Register(ch)
	}
	rctx, stop := context.WithCancel(r.deps.Parent)
	r.cancel = stop
	r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateRunning, "weixin running", cfg)
	r.mu.Unlock()

	go func() {
		err := ch.Start(rctx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		if r.deps.Notifier != nil {
			r.deps.Notifier.Unregister(PlatformWeixin)
		}
		r.cancel = nil
		message := "weixin stopped"
		state := pkgplugins.RuntimeStateStopped
		if err != nil && rctx.Err() == nil {
			message = err.Error()
			state = pkgplugins.RuntimeStateError
			r.deps.Log.Error("weixin: stopped unexpectedly", "error", err)
		}
		r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), state, message, cfg)
	}()

	return nil
}

func (r *weixinManagedRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.deps.Notifier != nil {
		r.deps.Notifier.Unregister(PlatformWeixin)
	}
	r.snapshot = weixinRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "weixin stopped", WeixinConfig{})
	return nil
}

func (r *weixinManagedRuntime) Snapshot(ctx context.Context) (pkgplugins.RuntimeSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}

func DecodeWeixinPluginConfig(raw map[string]any) (WeixinConfig, error) {
	if raw == nil {
		return WeixinConfig{}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return WeixinConfig{}, fmt.Errorf("marshal weixin config: %w", err)
	}
	var cfg WeixinConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return WeixinConfig{}, fmt.Errorf("decode weixin config: %w", err)
	}
	return cfg, nil
}

func RedactWeixinPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeWeixinPluginConfig(raw)
	if err != nil {
		return cloneWeixinConfigMap(raw)
	}
	out := cloneWeixinConfigMap(raw)
	if cfg.BotToken != "" {
		out["bot_token"] = "***"
	}
	return out
}

func weixinRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg WeixinConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"base_url":          cfg.BaseURL,
			"bot_id":            cfg.BotID,
			"user_id":           cfg.UserID,
			"notify_enabled":    cfg.EnableNotify,
			"has_bot_identity":  cfg.BotID != "",
			"has_user_identity": cfg.UserID != "",
		},
	}
}

func cloneWeixinConfigMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
