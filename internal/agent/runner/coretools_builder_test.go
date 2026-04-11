package runner

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
	bashtool "github.com/vaayne/anna/plugins/tools/bash"
	edittool "github.com/vaayne/anna/plugins/tools/edit"
	readtool "github.com/vaayne/anna/plugins/tools/read"
	writetool "github.com/vaayne/anna/plugins/tools/write"
)

type fakeSandboxBackend struct {
	boxsh *boxshclient.SharedBackend
}

func (f fakeSandboxBackend) Runtime() pkgplugins.SandboxRuntime {
	return plugintools.SandboxRuntimeFromBackend(nil)
}
func (f fakeSandboxBackend) Boxsh() *boxshclient.SharedBackend { return f.boxsh }
func (f fakeSandboxBackend) SessionDir() string                { return "" }
func (f fakeSandboxBackend) Alive() bool                       { return true }
func (f fakeSandboxBackend) Close() error                      { return nil }

type fakeRuntime struct{}

func (fakeRuntime) Enabled() bool { return false }
func (fakeRuntime) Exec(context.Context, string, int) (pkgplugins.SandboxExecResult, error) {
	return pkgplugins.SandboxExecResult{}, nil
}

// delegateBuilder creates regular (non-sandbox adapter) tools for testing.
func delegateBuilder(bc plugintools.BuildContext) []tools.Tool {
	return []tools.Tool{
		bashtool.NewBashTool(bc.WorkDir, bc.ToolsBinDir),
		&readtool.ReadTool{},
		&writetool.WriteTool{},
		&edittool.EditTool{},
	}
}

func TestCoreToolsBuilderWithSandbox_NoBackendUsesDelegate(t *testing.T) {
	builder := CoreToolsBuilderWithSandbox(delegateBuilder, fakeSandboxBackend{})
	bc := plugintools.BuildContext{
		WorkDir:     "/tmp",
		ToolsBinDir: "/tmp/bin",
		Sandbox:     fakeRuntime{},
	}

	tools := builder(bc)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}
	if _, ok := tools[0].(*bashtool.BashTool); !ok {
		t.Fatalf("expected delegate bash tool, got %T", tools[0])
	}
}

func TestCoreToolsBuilderWithSandbox_WithBackendUsesAdapters(t *testing.T) {
	builder := CoreToolsBuilderWithSandbox(delegateBuilder, fakeSandboxBackend{
		boxsh: &boxshclient.SharedBackend{},
	})
	bc := plugintools.BuildContext{
		WorkDir:     "/tmp",
		ToolsBinDir: "/tmp/bin",
		Sandbox:     fakeRuntime{},
	}

	tools := builder(bc)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}
	if _, ok := tools[0].(*boxshclient.BashAdapter); !ok {
		t.Fatalf("expected sandbox bash adapter, got %T", tools[0])
	}
}

func TestBuildSandboxCoreTools_NoBoxshReturnsNil(t *testing.T) {
	got := buildSandboxCoreTools(fakeSandboxBackend{}, plugintools.BuildContext{})
	if got != nil {
		t.Fatalf("expected nil core tools without boxsh backend, got %v", got)
	}
}
