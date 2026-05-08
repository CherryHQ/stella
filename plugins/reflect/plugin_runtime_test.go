package reflect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

type fakeService struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newFakeService() *fakeService {
	return &fakeService{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (s *fakeService) Start(ctx context.Context) error {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	close(s.stopped)
	return ctx.Err()
}

func (s *fakeService) RunOnce(ctx context.Context) {}

func TestManagedRuntimeApplyDisableReconfigure(t *testing.T) {
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	var (
		mu       sync.Mutex
		configs  []Config
		services []*fakeService
	)
	runtime := NewManagedRuntime(RuntimeDeps{
		Services: fakeReflectRuntimeServices{parent: context.Background()},
		Now:      func() time.Time { return now },
		NewService: func(cfg Config) serviceRunner {
			mu.Lock()
			defer mu.Unlock()
			configs = append(configs, cfg)
			svc := newFakeService()
			services = append(services, svc)
			return svc
		},
	}).(*managedRuntime)

	first := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"interval": "15m", "batch": 2}}
	if err := runtime.Apply(context.Background(), first); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	waitClosed(t, services[0].started, "first start")

	snap, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after first apply: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("state after first apply = %q, want %q", snap.State, pkgplugins.RuntimeStateRunning)
	}
	if got := snap.Metadata["interval"]; got != "15m0s" {
		t.Fatalf("interval metadata = %#v, want %q", got, "15m0s")
	}
	if got := snap.Metadata["batch"]; got != 2 {
		t.Fatalf("batch metadata = %#v, want 2", got)
	}

	second := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"interval": "45m", "batch": 9}}
	if err := runtime.Apply(context.Background(), second); err != nil {
		t.Fatalf("reconfigure apply: %v", err)
	}
	waitClosed(t, services[0].stopped, "first stop")
	waitClosed(t, services[1].started, "second start")

	mu.Lock()
	if len(configs) != 2 {
		t.Fatalf("len(configs) = %d, want 2", len(configs))
	}
	if configs[0].Interval != 15*time.Minute || configs[0].Batch != 2 {
		t.Fatalf("first config = %#v", configs[0])
	}
	if configs[1].Interval != 45*time.Minute || configs[1].Batch != 9 {
		t.Fatalf("second config = %#v", configs[1])
	}
	mu.Unlock()

	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{ID: PluginID, Enabled: false, Config: second.Config}); err != nil {
		t.Fatalf("disable apply: %v", err)
	}
	waitClosed(t, services[1].stopped, "second stop")

	snap, err = runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot after disable: %v", err)
	}
	if snap.State != pkgplugins.RuntimeStateStopped {
		t.Fatalf("state after disable = %q, want %q", snap.State, pkgplugins.RuntimeStateStopped)
	}
	if snap.Metadata["interval"] != "45m0s" {
		t.Fatalf("disabled interval metadata = %#v, want %q", snap.Metadata["interval"], "45m0s")
	}
	if snap.Metadata["batch"] != 9 {
		t.Fatalf("disabled batch metadata = %#v, want 9", snap.Metadata["batch"])
	}
}

func TestManagedRuntimeUsesSchedulerCapabilityWhenAvailable(t *testing.T) {
	scheduler := &fakeSchedulerService{}
	runtime := NewManagedRuntime(RuntimeDeps{
		Services:  fakeReflectRuntimeServices{parent: context.Background()},
		Scheduler: scheduler,
		Now:       func() time.Time { return time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC) },
		NewService: func(cfg Config) serviceRunner {
			t.Fatal("unexpected background service when scheduler capability is available")
			return nil
		},
	}).(*managedRuntime)

	state := pkgplugins.PluginState{ID: PluginID, Enabled: true, Config: map[string]any{"interval": "30m", "batch": 4}}
	if err := runtime.Apply(context.Background(), state); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if scheduler.reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", scheduler.reconcileCalls)
	}
	if len(scheduler.lastJobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(scheduler.lastJobs))
	}
	job := scheduler.lastJobs[0]
	if job.Key != "review" || job.RuntimeName != RuntimeName || job.Schedule.Every != "30m0s" {
		t.Fatalf("unexpected scheduled job: %#v", job)
	}

	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if scheduler.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", scheduler.deleteCalls)
	}
}

func TestDecodePluginConfigPreservesReviewLoopDefaults(t *testing.T) {
	cfg, err := DecodePluginConfig(nil)
	if err != nil {
		t.Fatalf("DecodePluginConfig(nil): %v", err)
	}
	if cfg.Interval != time.Hour {
		t.Fatalf("interval = %s, want %s", cfg.Interval, time.Hour)
	}
	if cfg.Batch != 5 {
		t.Fatalf("batch = %d, want 5", cfg.Batch)
	}

	cfg, err = DecodePluginConfig(map[string]any{"interval": "2h", "batch": float64(7)})
	if err != nil {
		t.Fatalf("DecodePluginConfig(custom): %v", err)
	}
	if cfg.Interval != 2*time.Hour || cfg.Batch != 7 {
		t.Fatalf("decoded config = %#v", cfg)
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

type fakeSchedulerService struct {
	reconcileCalls int
	deleteCalls    int
	lastJobs       []pkgplugins.SchedulerJobSpec
}

func (s *fakeSchedulerService) ReconcileJobs(_ context.Context, jobs []pkgplugins.SchedulerJobSpec) error {
	s.reconcileCalls++
	s.lastJobs = append([]pkgplugins.SchedulerJobSpec(nil), jobs...)
	return nil
}

func (s *fakeSchedulerService) DeleteJobs(context.Context) error {
	s.deleteCalls++
	return nil
}

func (*fakeSchedulerService) DeleteJob(context.Context, string) error { return nil }

func (*fakeSchedulerService) ListJobs(context.Context) ([]pkgplugins.SchedulerJob, error) {
	return nil, nil
}

type fakeReflectRuntimeServices struct {
	parent context.Context
}

func (s fakeReflectRuntimeServices) ParentContext() context.Context { return s.parent }
func (fakeReflectRuntimeServices) Memory() memory.Provider          { return nil }
func (fakeReflectRuntimeServices) Store() pkgplugins.ReflectStore   { return nil }
func (fakeReflectRuntimeServices) Workspace() string                { return "" }
func (fakeReflectRuntimeServices) BuildProviders(string, string, string) (*providers.Registry, error) {
	return nil, nil
}
