package pluginhost

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestRuntimeLookup(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"tool/mcp": {ID: "tool/mcp", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("tool/mcp")
	host.AddRuntime(pkgplugins.RuntimeSpec{PluginID: "tool/mcp", Name: "main", Build: func(ctx pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	if err := host.ApplyPlugin(context.Background(), "tool/mcp"); err != nil {
		t.Fatal(err)
	}
	handle, ok := host.Runtime().Get("tool/mcp", "main")
	if !ok {
		t.Fatal("expected runtime handle")
	}
	snap, err := handle.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != pkgplugins.RuntimeStateRunning {
		t.Fatalf("unexpected state: %s", snap.State)
	}
}
