package pluginhost

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

type testTool struct{ name string }

func (t *testTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t *testTool) Execute(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestBuildEnabledToolsBuildsAllOptionalToolsWithRuntimeContext(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{
		"tool/a": {ID: "tool/a", Enabled: true},
		"tool/b": {ID: "tool/b", Enabled: true},
	}}
	host := New(store)
	host.RegisterPluginID("tool/a")
	host.RegisterPluginID("tool/b")

	var runtimeSeen int
	fakeRuntime := pkgplugins.NewLocalToolRuntime(t.TempDir())
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/a",
		Name:     "a",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Runtime != nil {
				runtimeSeen++
			}
			return &testTool{name: "a"}, nil
		},
	})
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/b",
		Name:     "b",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Runtime != nil {
				runtimeSeen++
			}
			return &testTool{name: "b"}, nil
		},
	})

	got := host.BuildEnabledTools(context.Background(), plugintools.BuildContext{Runtime: fakeRuntime})
	if len(got) != 2 {
		t.Fatalf("BuildEnabledTools() len = %d, want 2", len(got))
	}
	if runtimeSeen != 2 {
		t.Fatalf("runtime seen = %d, want 2", runtimeSeen)
	}
}
