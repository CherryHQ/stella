package plugintools_test

import (
	"context"
	"testing"

	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

type testTool struct {
	name string
}

func (t *testTool) Definition() tools.Definition {
	return tools.Definition{Name: t.name, Description: "test"}
}

func (t *testTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "ok", nil
}

func TestNamesReturnsRegistered(t *testing.T) {
	// Since the package registry is global, we just check Names() returns strings.
	names := plugintools.Names()
	// import of plugins/tools registers read, write, edit, bash.
	// But we're in an external test package, so only the explicitly imported
	// packages from test deps will be registered.
	if names == nil {
		t.Error("Names() should never return nil")
	}
}

func TestRegisterAndBuildCore(t *testing.T) {
	plugintools.Register("test-required-tool", plugintools.Registration{
		Required: true,
		Factory: func(_ plugintools.BuildContext) (tools.Tool, error) {
			return &testTool{name: "test-required-tool"}, nil
		},
	})

	built := plugintools.BuildCore(plugintools.BuildContext{})
	var found bool
	for _, t := range built {
		if t.Definition().Name == "test-required-tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected test-required-tool in BuildCore output")
	}
}

func TestBuildEnabled(t *testing.T) {
	plugintools.Register("test-optional-tool", plugintools.Registration{
		Required: false,
		Factory: func(_ plugintools.BuildContext) (tools.Tool, error) {
			return &testTool{name: "test-optional-tool"}, nil
		},
	})

	// Enable only our test tool.
	built := plugintools.BuildEnabled(plugintools.BuildContext{}, func(name string) bool {
		return name == "test-optional-tool"
	})
	var found bool
	for _, t := range built {
		if t.Definition().Name == "test-optional-tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected test-optional-tool in BuildEnabled output")
	}
}
