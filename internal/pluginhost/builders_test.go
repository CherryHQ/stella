package pluginhost

import (
	"context"
	"runtime"
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

type testTool struct{ name string }

func (t *testTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t *testTool) Execute(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestBuildEnabledToolsSkipsUnsandboxedPluginToolsWhenBackendActive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("boxsh enforcement does not apply on windows")
	}

	store := &stubStore{plugins: map[string]config.Plugin{
		"tool/sandboxed":   {ID: "tool/sandboxed", Enabled: true},
		"tool/unsandboxed": {ID: "tool/unsandboxed", Enabled: true},
	}}
	host := New(store)
	host.RegisterPluginID("tool/sandboxed")
	host.RegisterPluginID("tool/unsandboxed")
	host.AddTool(pkgplugins.ToolSpec{
		PluginID:  "tool/sandboxed",
		Name:      "sandboxed",
		Sandboxed: true,
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			return &testTool{name: "sandboxed"}, nil
		},
	})
	host.AddTool(pkgplugins.ToolSpec{
		PluginID:  "tool/unsandboxed",
		Name:      "unsandboxed",
		Sandboxed: false,
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			return &testTool{name: "unsandboxed"}, nil
		},
	})

	got := host.BuildEnabledTools(context.Background(), plugintools.BuildContext{
		Backend: &boxshclient.SharedBackend{},
	})
	if len(got) != 1 || got[0].Definition().Name != "sandboxed" {
		t.Fatalf("BuildEnabledTools() = %#v, want only sandboxed tool", got)
	}
}

func TestBuildEnabledToolsIncludesUnsandboxedPluginToolsWithoutBackend(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{
		"tool/unsandboxed": {ID: "tool/unsandboxed", Enabled: true},
	}}
	host := New(store)
	host.RegisterPluginID("tool/unsandboxed")
	host.AddTool(pkgplugins.ToolSpec{
		PluginID:  "tool/unsandboxed",
		Name:      "unsandboxed",
		Sandboxed: false,
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			return &testTool{name: "unsandboxed"}, nil
		},
	})

	got := host.BuildEnabledTools(context.Background(), plugintools.BuildContext{})
	if len(got) != 1 || got[0].Definition().Name != "unsandboxed" {
		t.Fatalf("BuildEnabledTools() = %#v, want unsandboxed tool when backend is disabled", got)
	}
}
