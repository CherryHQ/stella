package pluginhooks_test

import (
	"testing"

	"github.com/vaayne/anna/pkg/hooks"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
)

type testHookPlugin struct {
	name string
}

func (t *testHookPlugin) Name() string  { return t.name }
func (t *testHookPlugin) Priority() int { return 1 }

func TestHooksRegisterAndNames(t *testing.T) {
	pluginhooks.Register("test-hook", pluginhooks.Registration{
		Factory: func(_ pluginhooks.BuildContext) (hooks.HookPlugin, error) {
			return &testHookPlugin{name: "test-hook"}, nil
		},
	})

	names := pluginhooks.Names()
	var found bool
	for _, n := range names {
		if n == "test-hook" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'test-hook' in Names()")
	}
}

func TestHooksBuildEnabled(t *testing.T) {
	pluginhooks.Register("enabled-hook", pluginhooks.Registration{
		Factory: func(_ pluginhooks.BuildContext) (hooks.HookPlugin, error) {
			return &testHookPlugin{name: "enabled-hook"}, nil
		},
	})

	plugins := pluginhooks.BuildEnabled(pluginhooks.BuildContext{}, func(name string) bool {
		return name == "enabled-hook"
	})
	var found bool
	for _, p := range plugins {
		if p.Name() == "enabled-hook" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'enabled-hook' in BuildEnabled output")
	}
}

func TestCloseHookPlugins_NoCloser(t *testing.T) {
	// Should not panic for plugins without io.Closer.
	pluginhooks.CloseHookPlugins([]hooks.HookPlugin{
		&testHookPlugin{name: "x"},
	})
}

// closerPlugin implements both HookPlugin and io.Closer.
type closerPlugin struct {
	testHookPlugin
	closed bool
}

func (c *closerPlugin) Close() error {
	c.closed = true
	return nil
}

func TestCloseHookPlugins_WithCloser(t *testing.T) {
	cp := &closerPlugin{testHookPlugin: testHookPlugin{name: "closer"}}
	pluginhooks.CloseHookPlugins([]hooks.HookPlugin{cp})
	if !cp.closed {
		t.Error("expected closerPlugin.Close() to be called")
	}
}
