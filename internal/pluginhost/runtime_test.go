package pluginhost

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestRuntimeLookup(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{"mcp": {ID: "mcp", Enabled: true}}}
	host := New(store)
	host.RegisterPluginID("mcp")
	host.RegisterRuntime(pkgplugins.RuntimeRegistration{PluginID: "mcp", Name: "main", Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
		return runtimeStub{apply: func(context.Context, pkgplugins.PluginState) error { return nil }}, nil
	}})
	if err := host.ApplyPlugin(context.Background(), "mcp"); err != nil {
		t.Fatal(err)
	}
	handle, ok := host.Runtime().Get("mcp", "main")
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
