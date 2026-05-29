package pluginhost

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/orgctx"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const testOrg = "org-A"

func ctxWithOrg(org string) context.Context {
	return orgctx.WithOrgID(context.Background(), org)
}

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
	ctx := ctxWithOrg(testOrg)
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

// TestRuntimeOrgIsolation verifies that two orgs each running a runtime under
// the same plugin ID + runtime name keep entirely separate managed instances.
func TestRuntimeOrgIsolation(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/test": {ID: "tool/test", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/test")

	var builds []string
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/test", Name: "main", Build: func(rc pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		builds = append(builds, rc.State.ID)
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})

	ctxA := ctxWithOrg("org-A")
	ctxB := ctxWithOrg("org-B")
	if err := host.ApplyPlugin(ctxA, "tool/test"); err != nil {
		t.Fatalf("ApplyPlugin org-A: %v", err)
	}
	if err := host.ApplyPlugin(ctxB, "tool/test"); err != nil {
		t.Fatalf("ApplyPlugin org-B: %v", err)
	}
	if len(builds) != 2 {
		t.Fatalf("expected 2 builds (one per org), got %d", len(builds))
	}

	// Each org sees its own runtime; cross-org lookup returns nothing.
	if _, ok := host.Runtime().Get(ctxA, "tool/test", "main"); !ok {
		t.Fatal("org-A: expected runtime handle")
	}
	if _, ok := host.Runtime().Get(ctxB, "tool/test", "main"); !ok {
		t.Fatal("org-B: expected runtime handle")
	}

	// Shutdown org-A clears only its runtimes; org-B still works.
	if err := host.Shutdown(ctxA); err != nil {
		t.Fatalf("Shutdown org-A: %v", err)
	}
	if _, ok := host.Runtime().Get(ctxA, "tool/test", "main"); ok {
		t.Fatal("org-A: expected runtime gone after Shutdown")
	}
	if _, ok := host.Runtime().Get(ctxB, "tool/test", "main"); !ok {
		t.Fatal("org-B: expected runtime still present after org-A Shutdown")
	}
}

// TestRuntimeMissingOrgIDOnReadReturnsFalse ensures the read paths fail soft
// when ctx lacks orgID (Get/Lookup return nil,false instead of panicking).
func TestRuntimeMissingOrgIDOnReadReturnsFalse(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	if _, ok := host.Runtime().Get(context.Background(), "tool/test", "main"); ok {
		t.Fatal("expected (nil, false) when ctx has no orgID")
	}
}

// TestRuntimeMissingOrgIDOnWriteReturnsError ensures Apply/Shutdown reject a
// ctx without orgID rather than silently accepting it.
func TestRuntimeMissingOrgIDOnWriteReturnsError(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/test": {ID: "tool/test", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/test")
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/test", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	if err := host.ApplyPlugin(context.Background(), "tool/test"); err == nil {
		t.Fatal("ApplyPlugin without orgID: expected error, got nil")
	}
	if err := host.Shutdown(context.Background()); err == nil {
		t.Fatal("Shutdown without orgID: expected error, got nil")
	}
}

// TestApplyChannelOrgMismatchRejects guards against using one org's ctx to
// apply another org's channel record.
func TestApplyChannelOrgMismatchRejects(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("channel/telegram")
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "channel/telegram", Name: "bot", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	ch := config.Channel{ID: "tg-main", Type: "telegram", Enabled: true, Config: "{}", OrgID: "org-B"}
	err := host.ApplyChannel(ctxWithOrg("org-A"), ch)
	if err == nil {
		t.Fatal("expected error for ctx/channel org mismatch")
	}
}

// TestShutdownReleasesLockBeforeStop ensures Stop() is called outside the
// RuntimeHost mutex; the stub's Stop reads back via Get to verify no deadlock.
func TestShutdownReleasesLockBeforeStop(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/test": {ID: "tool/test", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/test")
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/test", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return reentrantRuntime{host: host}, nil
	}})
	ctx := ctxWithOrg("org-A")
	if err := host.ApplyPlugin(ctx, "tool/test"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Shutdown(ctx) }()
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
