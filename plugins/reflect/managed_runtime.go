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
	RunOnce(ctx context.Context)
}

type RuntimeDeps struct {
	Services      pkgplugins.ReflectPlatform
	Notifications pkgplugins.Notifier
	StateStore    pkgplugins.StateStore
	Scheduler     pkgplugins.Scheduler
	Log           *slog.Logger
	NewService    func(Config) serviceRunner
	Now           func() time.Time
}

type managedRuntime struct {
	deps RuntimeDeps

	mu         sync.RWMutex
	cancel     context.CancelFunc
	generation int
	config     PluginConfig
	snapshot   pkgplugins.RuntimeStatus
}

func NewManagedRuntime(deps RuntimeDeps) pkgplugins.Runtime {
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
		snapshot: pkgplugins.RuntimeStatus{
			State:     pkgplugins.RuntimeStateStopped,
			UpdatedAt: deps.Now(),
			Metadata:  map[string]any{},
		},
	}
}

func (r *managedRuntime) Start(ctx context.Context, desired pkgplugins.PluginState) error {
	return r.Apply(ctx, desired)
}

func (r *managedRuntime) Reconcile(ctx context.Context, desired pkgplugins.PluginState) error {
	return r.Apply(ctx, desired)
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
	r.config = cfg
	if !desired.Enabled {
		r.snapshot = runtimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "reflect disabled", cfg)
		r.mu.Unlock()
		if r.deps.Scheduler != nil {
			return r.deps.Scheduler.DeleteJobs(ctx)
		}
		return nil
	}

	if r.deps.Scheduler != nil {
		r.snapshot = runtimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateRunning, "reflect scheduled", cfg)
		r.mu.Unlock()
		return r.deps.Scheduler.ReconcileJobs(ctx, []pkgplugins.SchedulerJobSpec{{
			Key:         "review",
			RuntimeName: RuntimeName,
			Name:        "Reflect Review",
			Description: "Run reflect conversation review.",
			Schedule: pkgplugins.SchedulerSchedule{
				Every: cfg.Interval.String(),
			},
			Payload: map[string]any{"batch": cfg.Batch},
			Enabled: true,
		}})
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
	r.generation++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	cfg := r.config
	if cfg.Interval <= 0 {
		cfg, _ = DecodePluginConfig(nil)
	}
	r.snapshot = runtimeSnapshot(r.deps.Now(), pkgplugins.RuntimeStateStopped, "reflect stopped", cfg)
	r.mu.Unlock()
	if r.deps.Scheduler != nil {
		return r.deps.Scheduler.DeleteJobs(ctx)
	}
	return nil
}

func (r *managedRuntime) Snapshot(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.Clone(), nil
}

func (r *managedRuntime) Status(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	return r.Snapshot(ctx)
}

func (r *managedRuntime) RuntimeAccessor() any { return r }

func (r *managedRuntime) RunScheduledJob(ctx context.Context, key string, payload map[string]any) error {
	if key != "review" {
		return fmt.Errorf("reflect: unknown scheduled job %q", key)
	}

	r.mu.RLock()
	cfg := r.config
	r.mu.RUnlock()
	if cfg.Interval <= 0 {
		cfg, _ = DecodePluginConfig(nil)
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
	svc.RunOnce(ctx)
	return nil
}

func runtimeSnapshot(now time.Time, state pkgplugins.RuntimeState, message string, cfg PluginConfig) pkgplugins.RuntimeStatus {
	return pkgplugins.RuntimeStatus{
		State:     state,
		Message:   message,
		UpdatedAt: now,
		Metadata: map[string]any{
			"interval": cfg.Interval.String(),
			"batch":    cfg.Batch,
		},
	}
}
