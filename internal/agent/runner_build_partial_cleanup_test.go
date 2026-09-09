package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

type retryCloseTool struct {
	name            string
	closeCalls      atomic.Int32
	failures        atomic.Int32
	panicFirst      bool
	panicDefinition bool
}

func (t *retryCloseTool) Definition() pkgtools.Definition {
	if t.panicDefinition {
		panic("definition panic")
	}
	return pkgtools.Definition{Name: t.name}
}

func (*retryCloseTool) Execute(context.Context, map[string]any) (string, error) { return "", nil }

func (t *retryCloseTool) Close() error {
	call := t.closeCalls.Add(1)
	if t.panicFirst && call == 1 {
		panic("close panic")
	}
	if call <= t.failures.Load() {
		return fmt.Errorf("close %s attempt %d", t.name, call)
	}
	return nil
}

func TestBuildToolRegistryKeepsRegisteredToolsOnPartialCloseFailure(t *testing.T) {
	first := &retryCloseTool{name: "first"}
	first.failures.Store(1)
	buildErr := errors.New("later builder failed")
	cleaned := false
	partial := &runner{cleanup: func() error { cleaned = true; return nil }}
	cfg := failClosedConfig(t)
	cfg.Partial = partial
	cfg.BuiltinTools = []BuiltinTool{
		{Tool: first},
		{Spec: pkgtools.Definition{Name: "later"}, Build: func(pkgplugins.ToolBuildContext) (pkgtools.Tool, error) {
			return nil, buildErr
		}},
	}

	_, _, _, err := buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	if !errors.Is(err, buildErr) {
		t.Fatalf("build error = %v, want %v", err, buildErr)
	}
	if partial.tools == nil {
		t.Fatal("partial runner lost registry after build failure")
	}
	if err := partial.Close(); err == nil {
		t.Fatal("first partial close succeeded despite a close failure")
	}
	if cleaned {
		t.Fatal("scratch cleanup ran while registry close was still failing")
	}
	if got := first.closeCalls.Load(); got != 1 {
		t.Fatalf("first close calls after failed cleanup = %d, want 1", got)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("retry partial close: %v", err)
	}
	if got := first.closeCalls.Load(); got != 2 {
		t.Fatalf("first close calls after retry = %d, want 2", got)
	}
	if !cleaned {
		t.Fatal("scratch cleanup did not run after successful retry")
	}
}

func TestBuildToolRegistryKeepsToolsOnBuilderPanic(t *testing.T) {
	first := &retryCloseTool{name: "first"}
	first.failures.Store(1)
	cleaned := false
	partial := &runner{cleanup: func() error { cleaned = true; return nil }}
	cfg := failClosedConfig(t)
	cfg.Partial = partial
	cfg.BuiltinTools = []BuiltinTool{
		{Tool: first},
		{Spec: pkgtools.Definition{Name: "later"}, Build: func(pkgplugins.ToolBuildContext) (pkgtools.Tool, error) {
			panic("later builder panicked")
		}},
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("builder panic was swallowed")
			}
		}()
		_, _, _, _ = buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	}()
	if err := partial.Close(); err == nil {
		t.Fatal("first partial close succeeded despite a close failure")
	}
	if cleaned {
		t.Fatal("scratch cleanup ran while registry close was still failing")
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("retry partial close after panic: %v", err)
	}
	if !cleaned {
		t.Fatal("scratch cleanup did not run after panic cleanup retry")
	}
}

func TestBuildToolRegistryOwnsBuiltinBeforeDefinitionPanic(t *testing.T) {
	built := &retryCloseTool{name: "definition-panic", panicDefinition: true}
	cleaned := false
	partial := &runner{cleanup: func() error { cleaned = true; return nil }}
	cfg := failClosedConfig(t)
	cfg.Partial = partial
	cfg.BuiltinTools = []BuiltinTool{{Spec: pkgtools.Definition{Name: built.name}, Build: func(pkgplugins.ToolBuildContext) (pkgtools.Tool, error) {
		return built, nil
	}}}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("definition panic was swallowed")
			}
		}()
		_, _, _, _ = buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, "")
	}()
	if got := built.closeCalls.Load(); got != 0 {
		t.Fatalf("builtin closed before partial owner retry: %d", got)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("close partial after definition panic: %v", err)
	}
	if got := built.closeCalls.Load(); got != 1 {
		t.Fatalf("builtin close calls = %d, want 1", got)
	}
	if !cleaned {
		t.Fatal("scratch cleanup did not run after definition panic cleanup")
	}
}

func TestBuildToolRegistryKeepsFilteredToolsOnPartialCloseFailure(t *testing.T) {
	filtered := &retryCloseTool{name: "filtered", panicFirst: true}
	cleaned := false
	partial := &runner{cleanup: func() error { cleaned = true; return nil }}
	cfg := failClosedConfig(t)
	cfg.Partial = partial
	cfg.PerRunTools = []pkgtools.Tool{filtered}
	cfg.ToolOverrideFetcher = func(context.Context, string, string) ([]ToolOverride, error) {
		return []ToolOverride{{Identity: ToolIdentity{CoreToolName: filtered.name}, Scope: ToolOverrideScopeUserAgent, Enabled: false}}, nil
	}

	if _, _, _, err := buildToolRegistry(context.Background(), cfg, &fakeSession{alive: true}, nil, ai.Model{}, ""); err != nil {
		t.Fatalf("build filtered tool registry: %v", err)
	}
	if cleaned {
		t.Fatal("scratch cleanup ran while filtered tool close was still pending")
	}
	if got := filtered.closeCalls.Load(); got != 1 {
		t.Fatalf("filtered close calls after panic = %d, want 1", got)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("retry filtered tool close: %v", err)
	}
	if got := filtered.closeCalls.Load(); got != 2 {
		t.Fatalf("filtered close calls after retry = %d, want 2", got)
	}
	if !cleaned {
		t.Fatal("scratch cleanup did not run after filtered tool retry")
	}
}
