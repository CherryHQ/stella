package channel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type BotRuntimeDeps[T any] struct {
	Parent               context.Context
	Handler              pkgchannel.Handler
	Notifier             *Dispatcher
	Log                  *slog.Logger
	Now                  func() time.Time
	Platform             string
	DecodeConfig         func(map[string]any) (T, error)
	ValidateConfig       func(T) string
	NotificationsEnabled func(T) bool
	NewChannel           func(T, pkgchannel.Handler) (pkgchannel.Channel, error)
	Snapshot             func(time.Time, pkgplugins.RuntimeState, string, T) pkgplugins.RuntimeSnapshot
}

type botManagedRuntime[T any] struct {
	deps BotRuntimeDeps[T]

	mu         sync.RWMutex
	cancel     context.CancelFunc
	generation int
	snapshot   pkgplugins.RuntimeSnapshot
}

func NewBotManagedRuntime[T any](deps BotRuntimeDeps[T]) *botManagedRuntime[T] {
	if deps.Parent == nil {
		deps.Parent = context.Background()
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.DecodeConfig == nil {
		panic("channel: missing bot runtime config decoder")
	}
	if deps.ValidateConfig == nil {
		deps.ValidateConfig = func(T) string { return "" }
	}
	if deps.NotificationsEnabled == nil {
		deps.NotificationsEnabled = func(T) bool { return false }
	}
	if deps.NewChannel == nil {
		panic("channel: missing bot runtime channel factory")
	}
	if deps.Snapshot == nil {
		panic("channel: missing bot runtime snapshot builder")
	}
	return &botManagedRuntime[T]{
		deps: deps,
		snapshot: pkgplugins.RuntimeSnapshot{
			State:     pkgplugins.RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *botManagedRuntime[T]) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	cfg, err := r.deps.DecodeConfig(desired.Config)
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
		r.deps.Notifier.Unregister(r.deps.Platform)
	}
	if !desired.Enabled {
		r.snapshot = r.deps.Snapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, r.deps.Platform+" disabled", cfg)
		r.mu.Unlock()
		return nil
	}
	if message := r.deps.ValidateConfig(cfg); message != "" {
		r.snapshot = r.deps.Snapshot(r.deps.Now(), pkgplugins.RuntimeStateError, message, cfg)
		r.mu.Unlock()
		return nil
	}

	ch, err := r.deps.NewChannel(cfg, r.deps.Handler)
	if err != nil {
		r.snapshot = r.deps.Snapshot(r.deps.Now(), pkgplugins.RuntimeStateError, err.Error(), cfg)
		r.mu.Unlock()
		return fmt.Errorf("build %s channel: %w", r.deps.Platform, err)
	}
	if ch.Name() != r.deps.Platform {
		r.snapshot = r.deps.Snapshot(r.deps.Now(), pkgplugins.RuntimeStateError, r.deps.Platform+": unexpected channel name", cfg)
		r.mu.Unlock()
		return fmt.Errorf("build %s channel: unexpected channel name %q", r.deps.Platform, ch.Name())
	}
	if r.deps.NotificationsEnabled(cfg) && r.deps.Notifier != nil {
		r.deps.Notifier.Register(ch)
	}
	childCtx, stop := context.WithCancel(r.deps.Parent)
	r.cancel = stop
	r.snapshot = r.deps.Snapshot(r.deps.Now(), pkgplugins.RuntimeStateRunning, r.deps.Platform+" running", cfg)
	r.mu.Unlock()

	go func() {
		err := ch.Start(childCtx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		if r.deps.Notifier != nil {
			r.deps.Notifier.Unregister(r.deps.Platform)
		}
		r.cancel = nil
		message := r.deps.Platform + " stopped"
		state := pkgplugins.RuntimeStateStopped
		if err != nil && childCtx.Err() == nil {
			message = err.Error()
			state = pkgplugins.RuntimeStateError
			r.deps.Log.Error(r.deps.Platform+": stopped unexpectedly", "error", err)
		}
		r.snapshot = r.deps.Snapshot(r.deps.Now(), state, message, cfg)
	}()

	return nil
}

func (r *botManagedRuntime[T]) Stop(ctx context.Context) error {
	var zero T

	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.deps.Notifier != nil {
		r.deps.Notifier.Unregister(r.deps.Platform)
	}
	r.snapshot = r.deps.Snapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, r.deps.Platform+" stopped", zero)
	return nil
}

func (r *botManagedRuntime[T]) Snapshot(ctx context.Context) (pkgplugins.RuntimeSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}
