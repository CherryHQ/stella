package runner

import (
	"runtime"
	"testing"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
	bashtool "github.com/vaayne/anna/plugins/tools/bash"
	edittool "github.com/vaayne/anna/plugins/tools/edit"
	readtool "github.com/vaayne/anna/plugins/tools/read"
	writetool "github.com/vaayne/anna/plugins/tools/write"
)

// delegateBuilder creates regular (non-boxsh) tools for testing.
func delegateBuilder(bc plugintools.BuildContext) []tools.Tool {
	return []tools.Tool{
		bashtool.NewBashTool(bc.WorkDir, bc.ToolsBinDir),
		&readtool.ReadTool{},
		&writetool.WriteTool{},
		&edittool.EditTool{},
	}
}

func TestCoreToolsBuilderWithBoxsh_WindowsUsesDelegate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific test")
	}

	builder := CoreToolsBuilderWithBoxsh(delegateBuilder)
	bc := plugintools.BuildContext{
		WorkDir:     "/tmp",
		ToolsBinDir: "/tmp/bin",
		Backend:     nil, // Even with a backend, Windows should use delegate
	}

	tools := builder(bc)
	if len(tools) != 4 {
		t.Errorf("expected 4 tools, got %d", len(tools))
	}

	// Verify we got the regular bash tool, not the boxsh adapter
	bashTool := tools[0]
	if _, ok := bashTool.(*bashtool.BashTool); !ok {
		t.Errorf("expected *bashtool.BashTool on Windows, got %T", bashTool)
	}
}

func TestCoreToolsBuilderWithBoxsh_NoBackendUsesDelegate(t *testing.T) {
	builder := CoreToolsBuilderWithBoxsh(delegateBuilder)
	bc := plugintools.BuildContext{
		WorkDir:     "/tmp",
		ToolsBinDir: "/tmp/bin",
		Backend:     nil, // No backend
	}

	tools := builder(bc)
	if len(tools) != 4 {
		t.Errorf("expected 4 tools, got %d", len(tools))
	}

	// Verify we got the regular bash tool
	bashTool := tools[0]
	if _, ok := bashTool.(*bashtool.BashTool); !ok {
		t.Errorf("expected *bashtool.BashTool without backend, got %T", bashTool)
	}
}

func TestCoreToolsBuilderWithBoxsh_WithBackendUsesBoxsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows - different behavior")
	}

	// We can't easily create a real backend without a boxsh binary,
	// but we can verify the function returns the right types when backend is non-nil
	builder := CoreToolsBuilderWithBoxsh(delegateBuilder)

	// Create a mock backend (we can't start it, but the type check is what we need)
	// Note: This will fail if we try to use it, but that's OK for this test
	backend := &boxshclient.SharedBackend{}

	bc := plugintools.BuildContext{
		WorkDir:     "/tmp",
		ToolsBinDir: "/tmp/bin",
		Backend:     backend,
	}

	tools := builder(bc)
	if len(tools) != 4 {
		t.Errorf("expected 4 tools, got %d", len(tools))
	}

	// On Linux/macOS with backend, we should get boxsh adapters
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		// Check that we got boxsh adapters, not regular tools
		if _, ok := tools[0].(*boxshclient.BashAdapter); !ok {
			t.Errorf("expected *boxshclient.BashAdapter with backend on %s, got %T", runtime.GOOS, tools[0])
		}
		if _, ok := tools[1].(*boxshclient.ReadAdapter); !ok {
			t.Errorf("expected *boxshclient.ReadAdapter with backend on %s, got %T", runtime.GOOS, tools[1])
		}
		if _, ok := tools[2].(*boxshclient.WriteAdapter); !ok {
			t.Errorf("expected *boxshclient.WriteAdapter with backend on %s, got %T", runtime.GOOS, tools[2])
		}
		if _, ok := tools[3].(*boxshclient.EditAdapter); !ok {
			t.Errorf("expected *boxshclient.EditAdapter with backend on %s, got %T", runtime.GOOS, tools[3])
		}
	}
}

func TestBuildBoxshCoreTools_NilBackend(t *testing.T) {
	// Test that buildBoxshCoreTools returns nil when backend is nil
	result := buildBoxshCoreTools(plugintools.BuildContext{Backend: nil})
	if result != nil {
		t.Errorf("expected nil when backend is nil, got %v", result)
	}
}

func TestCoreToolsBuilderWithBoxsh_NilDelegatePanics(t *testing.T) {
	// Test with nil delegate - should still work but return nil or panic
	builder := CoreToolsBuilderWithBoxsh(nil)

	// This should handle nil delegate gracefully
	bc := plugintools.BuildContext{
		WorkDir:     "/tmp",
		ToolsBinDir: "/tmp/bin",
		Backend:     nil,
	}

	// Should not panic even with nil delegate
	_ = builder(bc)
}
