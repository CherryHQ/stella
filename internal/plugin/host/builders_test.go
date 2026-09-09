package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/hooks"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/toolmeta"
	"github.com/CherryHQ/stella/pkg/tools"
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
	bindNativePolicy(t, host, store, "tool/a", "tool/b", "tool/c")
	store.plugins["tool/c"] = config.Plugin{ID: "tool/c", Enabled: false}

	var runtimeSeen int
	fakeRuntime := sandbox.NopSession()
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
		Required: true,
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Runtime != nil {
				runtimeSeen++
			}
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

	build := pkgplugins.ToolBuildContext{Runtime: fakeRuntime, AgentID: "agent"}
	got, err := host.BuildEnabledTools(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("BuildEnabledTools() len = %d, want 2", len(got))
	}
	if got[0].Definition().Name != "a" || got[1].Definition().Name != "b" {
		t.Fatalf("unexpected tools: %q, %q", got[0].Definition().Name, got[1].Definition().Name)
	}
	if runtimeSeen != 2 {
		t.Fatalf("runtime seen = %d, want 2", runtimeSeen)
	}
}

func TestToolMetadataKeepsEveryHostNameAndOwner(t *testing.T) {
	host := New(&stubStore{plugins: map[string]config.Plugin{}})
	host.RegisterPluginID("tool/host")
	host.AddTool(pkgplugins.ToolSpec{PluginID: "tool/host", Name: "host__owned"})
	host.AddTool(pkgplugins.ToolSpec{PluginID: "tool/host", Name: "host__disabled"})

	got := host.ToolMetadata()
	want := []toolmeta.ActionTool{
		{Name: "host__disabled", PluginID: "tool/host", LocalName: "host__disabled"},
		{Name: "host__owned", PluginID: "tool/host", LocalName: "host__owned"},
	}
	if len(got) != len(want) {
		t.Fatalf("metadata length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("metadata[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

type closableHostTool struct {
	testTool
	closed int
}

func (t *closableHostTool) Close() error { t.closed++; return nil }

type closableHostHook struct{ closed int }

func (h *closableHostHook) Name() string  { return "a" }
func (h *closableHostHook) Priority() int { return 0 }
func (h *closableHostHook) Close() error  { h.closed++; return nil }
func TestBuildFailureClosesPriorResources(t *testing.T) {
	want := errors.New("build failed")
	t.Run("tools", func(t *testing.T) {
		store := &stubStore{plugins: map[string]config.Plugin{}}
		h := New(store)
		h.RegisterPluginID("tool/a")
		bindNativePolicy(t, h, store, "tool/a")
		first := &closableHostTool{testTool: testTool{name: "a"}}
		partial := &closableHostTool{testTool: testTool{name: "b"}}
		h.AddTool(pkgplugins.ToolSpec{PluginID: "tool/a", Name: "a", Build: func(pkgplugins.ToolContext) (tools.Tool, error) { return first, nil }})
		h.AddTool(pkgplugins.ToolSpec{PluginID: "tool/a", Name: "b", Build: func(pkgplugins.ToolContext) (tools.Tool, error) { return partial, want }})
		got, err := h.BuildEnabledTools(t.Context(), pkgplugins.ToolBuildContext{AgentID: "agent"})
		if !errors.Is(err, want) || len(got) != 0 || first.closed != 1 || partial.closed != 1 {
			t.Fatalf("result=%v error=%v closed=%d/%d", got, err, first.closed, partial.closed)
		}
	})
	t.Run("hooks", func(t *testing.T) {
		store := &stubStore{plugins: map[string]config.Plugin{}}
		h := New(store)
		h.RegisterPluginID("tool/a")
		bindNativePolicy(t, h, store, "tool/a")
		first := &closableHostHook{}
		h.AddHook(pkgplugins.HookSpec{PluginID: "tool/a", Name: "a", Build: func(pkgplugins.HookContext) (hooks.HookPlugin, error) { return first, nil }})
		h.AddHook(pkgplugins.HookSpec{PluginID: "tool/a", Name: "b", Build: func(pkgplugins.HookContext) (hooks.HookPlugin, error) { return nil, want }})
		got, err := h.BuildEnabledHooks(t.Context(), "", "agent")
		if !errors.Is(err, want) || len(got) != 0 || first.closed != 1 {
			t.Fatalf("result=%v error=%v closed=%d", got, err, first.closed)
		}
	})
}

func TestBuildEnabledToolsRejectsNameDriftAndClosesTool(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{}}
	host := New(store)
	host.RegisterPluginID("tool/host")
	bindNativePolicy(t, host, store, "tool/host")
	tool := &closableHostTool{testTool: testTool{name: "built-name"}}
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/host",
		Name:     "declared-name",
		Build: func(pkgplugins.ToolContext) (tools.Tool, error) {
			return tool, nil
		},
	})

	got, err := host.BuildEnabledTools(t.Context(), pkgplugins.ToolBuildContext{AgentID: "agent"})
	if err == nil || !strings.Contains(err.Error(), "declared-name") || got != nil {
		t.Fatalf("result=%v error=%v, want name-drift failure", got, err)
	}
	if tool.closed != 1 {
		t.Fatalf("drifted tool close count = %d, want 1", tool.closed)
	}
}
