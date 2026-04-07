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
	telegramplugin "github.com/vaayne/anna/plugins/channels/telegram"
)

const (
	TelegramPluginID    = "channel/telegram"
	TelegramRuntimeName = "bot"
)

type TelegramRuntimeDeps struct {
	Parent     context.Context
	Handler    pkgchannel.Handler
	Notifier   *Dispatcher
	Log        *slog.Logger
	Now        func() time.Time
	NewChannel func(TelegramConfig, pkgchannel.Handler) (pkgchannel.Channel, error)
}

type telegramManagedRuntime struct {
	deps TelegramRuntimeDeps

	mu         sync.RWMutex
	cancel     context.CancelFunc
	generation int
	snapshot   pkgplugins.RuntimeSnapshot
}

func NewTelegramManagedRuntime(deps TelegramRuntimeDeps) pkgplugins.ManagedRuntime {
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
		deps.NewChannel = func(cfg TelegramConfig, handler pkgchannel.Handler) (pkgchannel.Channel, error) {
			return telegramplugin.New(telegramplugin.Config{
				Token:     cfg.Token,
				ChannelID: cfg.ChannelID,
				GroupMode: cfg.GroupMode,
			}, handler)
		}
	}
	return &telegramManagedRuntime{
		deps: deps,
		snapshot: pkgplugins.RuntimeSnapshot{
			State:     pkgplugins.RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *telegramManagedRuntime) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	cfg, err := DecodeTelegramPluginConfig(desired.Config)
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
		r.deps.Notifier.Unregister(PlatformTelegram)
	}
	if !desired.Enabled {
		r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "telegram disabled", cfg)
		r.mu.Unlock()
		return nil
	}
	if cfg.Token == "" {
		r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, "telegram: missing token", cfg)
		r.mu.Unlock()
		return nil
	}

	ch, err := r.deps.NewChannel(cfg, r.deps.Handler)
	if err != nil {
		r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, err.Error(), cfg)
		r.mu.Unlock()
		return fmt.Errorf("build telegram channel: %w", err)
	}
	if ch.Name() != PlatformTelegram {
		r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateError, "telegram: unexpected channel name", cfg)
		r.mu.Unlock()
		return fmt.Errorf("build telegram channel: unexpected channel name %q", ch.Name())
	}
	if cfg.EnableNotify && r.deps.Notifier != nil {
		r.deps.Notifier.Register(ch)
	}
	rctx, stop := context.WithCancel(r.deps.Parent)
	r.cancel = stop
	r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateRunning, "telegram running", cfg)
	r.mu.Unlock()

	go func() {
		err := ch.Start(rctx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		if r.deps.Notifier != nil {
			r.deps.Notifier.Unregister(PlatformTelegram)
		}
		r.cancel = nil
		message := "telegram stopped"
		state := pkgplugins.RuntimeStateStopped
		if err != nil && rctx.Err() == nil {
			message = err.Error()
			state = pkgplugins.RuntimeStateError
			r.deps.Log.Error("telegram: stopped unexpectedly", "error", err)
		}
		r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), state, message, cfg)
	}()

	return nil
}

func (r *telegramManagedRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.deps.Notifier != nil {
		r.deps.Notifier.Unregister(PlatformTelegram)
	}
	r.snapshot = telegramRuntimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "telegram stopped", TelegramConfig{})
	return nil
}

func (r *telegramManagedRuntime) Snapshot(ctx context.Context) (pkgplugins.RuntimeSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}

func DecodeTelegramPluginConfig(raw map[string]any) (TelegramConfig, error) {
	if raw == nil {
		return TelegramConfig{}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return TelegramConfig{}, fmt.Errorf("marshal telegram config: %w", err)
	}
	var cfg TelegramConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return TelegramConfig{}, fmt.Errorf("decode telegram config: %w", err)
	}
	return cfg, nil
}

func RedactTelegramPluginConfig(raw map[string]any) map[string]any {
	cfg, err := DecodeTelegramPluginConfig(raw)
	if err != nil {
		return cloneTelegramConfigMap(raw)
	}
	out := cloneTelegramConfigMap(raw)
	if cfg.Token != "" {
		out["token"] = "***"
	}
	return out
}

func telegramRuntimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg TelegramConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"channel_id":          cfg.ChannelID,
			"group_mode":          cfg.GroupMode,
			"notify_enabled":      cfg.EnableNotify,
			"has_default_channel": cfg.ChannelID != "",
		},
	}
}

func cloneTelegramConfigMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
