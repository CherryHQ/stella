package pluginhooks

import (
	"errors"
	"testing"

	"github.com/vaayne/anna/pkg/hooks"
)

type mockHook struct {
	name string
}

func (m *mockHook) Name() string  { return m.name }
func (m *mockHook) Priority() int { return 0 }

type closableHook struct {
	mockHook
	closed   bool
	closeErr error
}

func (c *closableHook) Close() error {
	c.closed = true
	return c.closeErr
}

func TestCloseHookPlugins_NonClosable(t *testing.T) {
	plugins := []hooks.HookPlugin{&mockHook{name: "noop"}}
	// Should not panic or error for non-closer
	CloseHookPlugins(plugins)
}

func TestCloseHookPlugins_Closable(t *testing.T) {
	p := &closableHook{mockHook: mockHook{name: "trace"}}
	CloseHookPlugins([]hooks.HookPlugin{p})
	if !p.closed {
		t.Fatal("expected Close() to be called")
	}
}

func TestCloseHookPlugins_CloseError(t *testing.T) {
	p := &closableHook{mockHook: mockHook{name: "err"}, closeErr: errors.New("close failed")}
	// Should log but not panic
	CloseHookPlugins([]hooks.HookPlugin{p})
	if !p.closed {
		t.Fatal("Close() should still have been called")
	}
}

func TestCloseHookPlugins_Empty(t *testing.T) {
	CloseHookPlugins(nil)
	CloseHookPlugins([]hooks.HookPlugin{})
}
