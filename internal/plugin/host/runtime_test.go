package host

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestConfigMapFromJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"empty string", "", map[string]any{}},
		{"valid json", `{"key":"val"}`, map[string]any{"key": "val"}},
		{"invalid json", `not json`, map[string]any{}},
		{"null json", `null`, map[string]any{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := configMapFromJSON(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("configMapFromJSON(%q): got %v, want %v", tc.in, got, tc.want)
			}
			for k, wv := range tc.want {
				if gv, ok := got[k]; !ok || gv != wv {
					t.Fatalf("configMapFromJSON(%q)[%q] = %v, want %v", tc.in, k, gv, wv)
				}
			}
		})
	}
}

func TestRuntimeLookup(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/test": {ID: "tool/test", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/test")
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/test", Name: "main", Build: func(ctx pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	ctx := context.Background()
	if err := host.ApplyPlugin(ctx, "tool/test"); err != nil {
		t.Fatal(err)
	}
	handle, ok := host.Runtime().Get(ctx, "tool/test", "main")
	if !ok {
		t.Fatal("expected runtime handle")
	}
	snap, err := handle.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("unexpected state: %s", snap.State)
	}
}

// TestRuntimeMissingKeyReturnsFalse ensures Get returns (nil,false) for
// unknown runtime keys.
func TestRuntimeMissingKeyReturnsFalse(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	if _, ok := host.Runtime().Get(context.Background(), "tool/test", "main"); ok {
		t.Fatal("expected (nil, false) for unknown runtime key")
	}
}

// TestShutdownReleasesLockBeforeStop ensures Stop() is called outside the
// RuntimeHost mutex; the stub's Stop reads back via Get to verify no deadlock.
func TestStopDuringApplyCleansLateRuntime(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/test": {ID: "tool/test", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/test")
	entered := make(chan struct{})
	release := make(chan struct{})
	var stops atomic.Int32
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/test", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return &blockingApplyRuntime{entered: entered, release: release, stops: &stops}, nil
	}})

	applyDone := make(chan error, 1)
	go func() { applyDone <- host.ApplyPlugin(context.Background(), "tool/test") }()
	<-entered
	if err := host.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(release)
	if err := <-applyDone; err == nil || !strings.Contains(err.Error(), "revoked while starting") {
		t.Fatalf("Apply error=%v, want revoked generation", err)
	}
	if got := stops.Load(); got < 2 {
		t.Fatalf("Stop calls=%d, want initial stop plus late-runtime cleanup", got)
	}
	if _, ok := host.Runtime().Get(context.Background(), "tool/test", "main"); ok {
		t.Fatal("revoked runtime escaped into the active table")
	}
}

type blockingApplyRuntime struct {
	entered chan struct{}
	release chan struct{}
	stops   *atomic.Int32
}

func (r *blockingApplyRuntime) Apply(context.Context, pkgplugins.PluginState) error {
	close(r.entered)
	<-r.release
	return nil
}
func (r *blockingApplyRuntime) Start(context.Context, pkgplugins.PluginState) error     { return nil }
func (r *blockingApplyRuntime) Reconcile(context.Context, pkgplugins.PluginState) error { return nil }
func (r *blockingApplyRuntime) Stop(context.Context) error                              { r.stops.Add(1); return nil }
func (r *blockingApplyRuntime) Snapshot(context.Context) (pkgplugins.RuntimeStatus, error) {
	return pkgplugins.RuntimeStatus{State: pkgplugins.RuntimeStateRunning}, nil
}

func (r *blockingApplyRuntime) Status(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	return r.Snapshot(ctx)
}

func TestShutdownReleasesLockBeforeStop(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/test": {ID: "tool/test", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/test")
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/test", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return reentrantRuntime{host: host}, nil
	}})
	ctx := context.Background()
	if err := host.ApplyPlugin(ctx, "tool/test"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Stop(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown deadlocked: lock held during Stop")
	}
}

// reentrantRuntime's Stop callbacks back into RuntimeHost.Get; if Shutdown held
// the lock during Stop, this would deadlock.
type reentrantRuntime struct{ host *Host }

func (r reentrantRuntime) Apply(context.Context, pkgplugins.PluginState) error     { return nil }
func (r reentrantRuntime) Start(context.Context, pkgplugins.PluginState) error     { return nil }
func (r reentrantRuntime) Reconcile(context.Context, pkgplugins.PluginState) error { return nil }
func (r reentrantRuntime) Stop(ctx context.Context) error {
	// During Stop, Shutdown has already removed the entry; this lookup should
	// return (nil,false) without deadlocking on the RuntimeHost mutex.
	_, _ = r.host.Runtime().Get(ctx, "tool/test", "main")
	return nil
}

func (r reentrantRuntime) Snapshot(context.Context) (pkgplugins.RuntimeStatus, error) {
	return pkgplugins.RuntimeStatus{State: pkgplugins.RuntimeStateRunning}, nil
}

func (r reentrantRuntime) Status(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	return r.Snapshot(ctx)
}
