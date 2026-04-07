package reflect

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
)

const (
	PluginID            = "reflect"
	RuntimeName         = "service"
	defaultReviewBatch  = 5
	defaultReviewPeriod = time.Hour
)

type PluginConfig struct {
	Interval time.Duration
	Batch    int
}

func DefaultPluginConfig() map[string]any {
	return map[string]any{}
}

func DecodePluginConfig(raw map[string]any) (PluginConfig, error) {
	cfg := PluginConfig{
		Interval: defaultReviewPeriod,
		Batch:    defaultReviewBatch,
	}
	if raw == nil {
		return cfg, nil
	}
	if v, ok := raw["interval"]; ok {
		interval, err := decodeDuration(v)
		if err != nil {
			return PluginConfig{}, fmt.Errorf("interval: %w", err)
		}
		if interval <= 0 {
			return PluginConfig{}, fmt.Errorf("interval: must be greater than 0")
		}
		cfg.Interval = interval
	}
	if v, ok := raw["batch"]; ok {
		batch, err := decodeInt(v)
		if err != nil {
			return PluginConfig{}, fmt.Errorf("batch: %w", err)
		}
		if batch <= 0 {
			return PluginConfig{}, fmt.Errorf("batch: must be greater than 0")
		}
		cfg.Batch = batch
	}
	return cfg, nil
}

func RedactPluginConfig(raw map[string]any) map[string]any {
	return cloneMap(raw)
}

type serviceRunner interface {
	Start(ctx context.Context) error
}

type RuntimeDeps struct {
	Parent     context.Context
	DB         *sql.DB
	Memory     memory.Provider
	Store      config.Store
	Notifier   *channel.Dispatcher
	Workspace  string
	Log        *slog.Logger
	Providers  func(api, apiKey, baseURL string) (*providers.Registry, error)
	NewService func(Config) serviceRunner
	Now        func() time.Time
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
	if deps.Parent == nil {
		deps.Parent = context.Background()
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
		DB:        r.deps.DB,
		Memory:    r.deps.Memory,
		Store:     r.deps.Store,
		Notifier:  r.deps.Notifier,
		Workspace: r.deps.Workspace,
		Interval:  cfg.Interval,
		Batch:     cfg.Batch,
		Log:       r.deps.Log,
		Providers: r.deps.Providers,
	})
	rctx, stop := context.WithCancel(r.deps.Parent)
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

func decodeDuration(value any) (time.Duration, error) {
	text, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("must be a duration string")
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func decodeInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("must be an integer")
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("must be an integer")
	}
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
