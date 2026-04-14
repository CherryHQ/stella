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
	var executions []pkgplugins.ExecutionContext
	var discoveries []pkgplugins.DiscoveryContext
	fakeRuntime := pkgplugins.NewLocalToolRuntime(t.TempDir())
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/a",
		Name:     "a",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Runtime != nil {
				runtimeSeen++
			}
			executions = append(executions, ctx.Execution)
			discoveries = append(discoveries, ctx.Discovery)
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
			executions = append(executions, ctx.Execution)
			discoveries = append(discoveries, ctx.Discovery)
			return &testTool{name: "b"}, nil
		},
	})

	build := plugintools.BuildContext{
		Execution: plugintools.ExecutionContext{WorkDir: "/work", UserRoot: "/user", ToolsBinDir: "/tools/bin"},
		Discovery: plugintools.DiscoveryContext{AnnaHome: "/anna", AgentRoot: "/agent", ProjectRoot: "/project", UserRoot: "/user"},
		Runtime:   fakeRuntime,
	}
	got := host.BuildEnabledTools(context.Background(), build)
	if len(got) != 2 {
		t.Fatalf("BuildEnabledTools() len = %d, want 2", len(got))
	}
	if runtimeSeen != 2 {
		t.Fatalf("runtime seen = %d, want 2", runtimeSeen)
	}
	for i, execution := range executions {
		if execution.WorkDir != build.Execution.WorkDir || execution.UserRoot != build.Execution.UserRoot || execution.ToolsBinDir != build.Execution.ToolsBinDir {
			t.Fatalf("execution[%d] = %+v, want %+v", i, execution, build.Execution)
		}
	}
	for i, discovery := range discoveries {
		if discovery.AnnaHome != build.Discovery.AnnaHome || discovery.AgentRoot != build.Discovery.AgentRoot || discovery.ProjectRoot != build.Discovery.ProjectRoot || discovery.UserRoot != build.Discovery.UserRoot {
			t.Fatalf("discovery[%d] = %+v, want %+v", i, discovery, build.Discovery)
		}
	}
}
