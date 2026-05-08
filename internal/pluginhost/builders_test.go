package pluginhost

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
	plugintools "github.com/CherryHQ/stella/plugins/tools"
)

type testTool struct{ name string }

func (t *testTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t *testTool) Execute(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestBuildEnabledToolsBuildsOptionalAndRequiredToolsWithRuntimeContext(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{
		"tool/a": {ID: "tool/a", Enabled: true},
		"tool/b": {ID: "tool/b", Enabled: true},
	}}
	host := New(store)
	host.RegisterPluginID("tool/a")
	host.RegisterPluginID("tool/b")
	host.RegisterPluginID("tool/c")

	var runtimeSeen int
	var seenPaths []pkgplugins.ToolPaths
	fakeRuntime := sandbox.NopSession()
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/a",
		Name:     "a",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Runtime != nil {
				runtimeSeen++
			}
			seenPaths = append(seenPaths, ctx.Paths)
			return &testTool{name: "a"}, nil
		},
	})
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/b",
		Name:     "b",
		Required: true,
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Runtime != nil {
				runtimeSeen++
			}
			seenPaths = append(seenPaths, ctx.Paths)
			return &testTool{name: "b"}, nil
		},
	})
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/c",
		Name:     "c",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			t.Fatal("disabled optional tool should not be built")
			return nil, nil
		},
	})

	build := plugintools.BuildContext{
		Paths: pkgplugins.ToolPaths{
			UserRoot:    "/user",
			ToolsBinDir: "/tools/bin",
			StellaHome:  "/stella",
			AgentRoot:   "/agent",
			ProjectRoot: "/project",
		},
		Runtime: fakeRuntime,
	}
	got := host.BuildEnabledTools(context.Background(), build)
	if len(got) != 2 {
		t.Fatalf("BuildEnabledTools() len = %d, want 2", len(got))
	}
	if got[0].Definition().Name != "a" || got[1].Definition().Name != "b" {
		t.Fatalf("unexpected tools: %q, %q", got[0].Definition().Name, got[1].Definition().Name)
	}
	if runtimeSeen != 2 {
		t.Fatalf("runtime seen = %d, want 2", runtimeSeen)
	}
	for i, paths := range seenPaths {
		if paths != build.Paths {
			t.Fatalf("paths[%d] = %+v, want %+v", i, paths, build.Paths)
		}
	}
}
