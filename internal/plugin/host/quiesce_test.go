package host

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// quiescerRuntimeStub is a runtime that records Quiesce invocations.
type quiescerRuntimeStub struct {
	runtimeStub
	quiesced *int32
}

func (q quiescerRuntimeStub) Quiesce(context.Context) { atomic.AddInt32(q.quiesced, 1) }

// nonQuiescerRuntimeStub is a runtime that does NOT implement the ingress
// quiescer; Quiesce must skip it without error.
type nonQuiescerRuntimeStub struct{ runtimeStub }

func TestRuntimeHostQuiesceInvokesQuiescersWithoutClearingTable(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{
		"tool/q": {ID: "tool/q", Enabled: true},
		"tool/n": {ID: "tool/n", Enabled: true},
	}}
	host := New(store)
	host.RegisterPluginID("tool/q")
	host.RegisterPluginID("tool/n")

	var quiesced int32
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/q", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return quiescerRuntimeStub{
			runtimeStub: runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }},
			quiesced:    &quiesced,
		}, nil
	}})
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/n", Name: "main", Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return nonQuiescerRuntimeStub{runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}}, nil
	}})

	ctx := context.Background()
	if err := host.ApplyPlugin(ctx, "tool/q"); err != nil {
		t.Fatal(err)
	}
	if err := host.ApplyPlugin(ctx, "tool/n"); err != nil {
		t.Fatal(err)
	}

	host.Quiesce(ctx)

	if got := atomic.LoadInt32(&quiesced); got != 1 {
		t.Fatalf("quiescer invoked %d times, want 1", got)
	}
	// The table must remain intact so a later Stop can tear runtimes down.
	if _, ok := host.Runtime().Get(ctx, "tool/q", "main"); !ok {
		t.Fatal("Quiesce cleared the runtime table (tool/q)")
	}
	if _, ok := host.Runtime().Get(ctx, "tool/n", "main"); !ok {
		t.Fatal("Quiesce cleared the runtime table (tool/n)")
	}

	// The host-level drain is terminal and idempotent: it must neither invoke
	// runtime quiescers twice nor allow a late apply to restart ingress.
	host.Quiesce(ctx)
	if got := atomic.LoadInt32(&quiesced); got != 1 {
		t.Fatalf("second Quiesce invoked quiescer %d times total, want 1", got)
	}
	if err := host.ApplyPlugin(ctx, "tool/q"); err == nil {
		t.Fatal("ApplyPlugin after Quiesce must be rejected")
	}
}
