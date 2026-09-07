package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type countingHook struct {
	name   string
	closed atomic.Int32
}

func (h *countingHook) Name() string { return h.name }
func (*countingHook) Priority() int  { return 0 }
func (h *countingHook) Close() error { h.closed.Add(1); return nil }

func TestReloadPluginHooksWithoutAgentsRetiresCachedGeneration(t *testing.T) {
	pm := NewPoolManager(nil, memorytest.New())
	initial := &countingHook{name: "initial"}
	pm.hookPlugins = []hooks.HookPlugin{initial}
	pm.pluginHooksBuilder = func(context.Context, string) ([]hooks.HookPlugin, error) {
		t.Fatal("native hooks must not be built without an Agent identity")
		return nil, nil
	}
	if err := pm.ReloadPluginHooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pm.Close(); err != nil {
		t.Fatal(err)
	}
	if got := initial.closed.Load(); got != 1 {
		t.Fatalf("hook generation %q close count = %d, want 1", initial.name, got)
	}
}
