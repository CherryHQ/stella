package pluginhost

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

type testTool struct{ name string }

func (t *testTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t *testTool) Execute(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestBuildEnabledToolsBuildsAllOptionalToolsWithSandboxContext(t *testing.T) {
	store := &stubStore{plugins: map[string]config.Plugin{
		"tool/a": {ID: "tool/a", Enabled: true},
		"tool/b": {ID: "tool/b", Enabled: true},
	}}
	host := New(store)
	host.RegisterPluginID("tool/a")
	host.RegisterPluginID("tool/b")

	var sandboxSeen int
	var hostSeen int
	fakeHost := &pluginHostTestHost{}
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/a",
		Name:     "a",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Sandbox != nil {
				sandboxSeen++
			}
			if ctx.Host != nil {
				hostSeen++
			}
			return &testTool{name: "a"}, nil
		},
	})
	host.AddTool(pkgplugins.ToolSpec{
		PluginID: "tool/b",
		Name:     "b",
		Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			if ctx.Sandbox != nil {
				sandboxSeen++
			}
			if ctx.Host != nil {
				hostSeen++
			}
			return &testTool{name: "b"}, nil
		},
	})

	got := host.BuildEnabledTools(context.Background(), plugintools.BuildContext{
		Sandbox: plugintools.SandboxRuntimeFromBackend(nil),
		Host:    fakeHost,
	})
	if len(got) != 2 {
		t.Fatalf("BuildEnabledTools() len = %d, want 2", len(got))
	}
	if sandboxSeen != 2 {
		t.Fatalf("sandbox seen = %d, want 2", sandboxSeen)
	}
	if hostSeen != 2 {
		t.Fatalf("host seen = %d, want 2", hostSeen)
	}
}

type pluginHostTestHost struct{}

func (h *pluginHostTestHost) ReadFile(context.Context, string, int, int) (sandbox.ReadResult, error) {
	return sandbox.ReadResult{}, nil
}

func (h *pluginHostTestHost) WriteFile(context.Context, string, []byte) (sandbox.WriteResult, error) {
	return sandbox.WriteResult{}, nil
}

func (h *pluginHostTestHost) EditFile(context.Context, string, []sandbox.Edit) (sandbox.EditResult, error) {
	return sandbox.EditResult{}, nil
}

func (h *pluginHostTestHost) Stat(context.Context, string) (sandbox.StatResult, error) {
	return sandbox.StatResult{}, nil
}

func (h *pluginHostTestHost) ListDir(context.Context, string) ([]sandbox.DirEntry, error) {
	return nil, nil
}
func (h *pluginHostTestHost) MkdirAll(context.Context, string, uint32) error { return nil }
func (h *pluginHostTestHost) Remove(context.Context, string, bool) error     { return nil }
func (h *pluginHostTestHost) Rename(context.Context, string, string) error   { return nil }
func (h *pluginHostTestHost) CreateTemp(context.Context, string, string) (sandbox.TempFile, error) {
	return nil, nil
}

func (h *pluginHostTestHost) Exec(context.Context, string, sandbox.ExecOptions) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{}, nil
}

func (h *pluginHostTestHost) StartProcess(context.Context, sandbox.ProcessRequest) (sandbox.ProcessHandle, error) {
	return nil, nil
}

func (h *pluginHostTestHost) HTTPRequest(context.Context, sandbox.HTTPOptions) (sandbox.HTTPResult, error) {
	return sandbox.HTTPResult{}, nil
}

func (h *pluginHostTestHost) OpenHTTPStream(context.Context, sandbox.HTTPOptions) (sandbox.HTTPStream, error) {
	return nil, nil
}
func (h *pluginHostTestHost) ResolvePath(path string) (string, error) { return path, nil }
func (h *pluginHostTestHost) WorkingDir() string                      { return "/tmp" }
