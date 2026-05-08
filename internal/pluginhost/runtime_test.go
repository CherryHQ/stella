package pluginhost

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
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
