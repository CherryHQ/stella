package reflect

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type serviceRunner interface {
	Start(ctx context.Context) error
}

type RuntimeDeps struct {
	Services      pkgplugins.ReflectRuntimeServices
	Notifications pkgplugins.NotificationService
	StateStore    pkgplugins.PluginStateStore
	Log           *slog.Logger
	NewService    func(Config) serviceRunner
	Now           func() time.Time
}

type managedRuntime struct {
	deps RuntimeDeps

	mu         sync.RWMutex
	cancel     context.CancelFunc
	generation int
	snapshot   pkgplugins.RuntimeSnapshot
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.ManagedRuntime {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.NewService == nil {
		deps.NewService = func(cfg Config) serviceRunner { return New(cfg) }
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	return &managedRuntime{
		deps: deps,
		snapshot: pkgplugins.RuntimeSnapshot{
			State:     pkgplugins.RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *managedRuntime) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	if r.deps.Services == nil {
		return fmt.Errorf("reflect: runtime services unavailable")
	}
	cfg, err := DecodePluginConfig(desired.Config)
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
	if !desired.Enabled {
		r.snapshot = runtimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "reflect disabled", cfg)
		r.mu.Unlock()
		return nil
	}

	svc := r.deps.NewService(Config{
		StateStore: r.deps.StateStore,
		Memory:     r.deps.Services.Memory(),
		Store:      r.deps.Services.Store(),
		Notifier:   r.deps.Notifications,
		Workspace:  r.deps.Services.Workspace(),
		Interval:   cfg.Interval,
		Batch:      cfg.Batch,
		Log:        r.deps.Log,
		Providers:  r.deps.Services.BuildProviders,
	})
	parent := r.deps.Services.ParentContext()
	if parent == nil {
		parent = context.Background()
	}
	rctx, stop := context.WithCancel(parent)
	r.cancel = stop
	r.snapshot = runtimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateRunning, "reflect running", cfg)
	r.mu.Unlock()

	go func() {
		err := svc.Start(rctx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		if rctx.Err() != nil {
			r.cancel = nil
			return
		}
		message := "reflect stopped"
		state := pkgplugins.RuntimeStateStopped
		if err != nil {
			message = err.Error()
			state = pkgplugins.RuntimeStateError
			r.deps.Log.Error("reflect: stopped unexpectedly", "error", err)
		}
		r.cancel = nil
		r.snapshot = runtimeSnapshot(r.deps.Now(), state, message, cfg)
	}()

	return nil
}

func (r *managedRuntime) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	cfg, _ := DecodePluginConfig(nil)
	r.snapshot = runtimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "reflect stopped", cfg)
	return nil
}

func (r *managedRuntime) Snapshot(ctx context.Context) (pkgplugins.RuntimeSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg PluginConfig) pkgplugins.RuntimeSnapshot {
	return pkgplugins.RuntimeSnapshot{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"interval": cfg.Interval.String(),
			"batch":    cfg.Batch,
		},
	}
}
