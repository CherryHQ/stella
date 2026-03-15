package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func newTestTool(name string) Tool {
	return Tool{
		Name:        name,
		Description: "test tool " + name,
		InputSchema: map[string]any{"type": "object"},
		Execute: func(_ context.Context, _ map[string]any) (string, error) {
			return "ok", nil
		},
	}
}

func TestRegisterTool(t *testing.T) {
	r := NewRegistry(nil)
	tool := newTestTool("my_tool")

	if err := r.RegisterTool(tool); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools := r.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	got, ok := tools["my_tool"]
	if !ok {
		t.Fatal("tool 'my_tool' not found")
	}
	if got.Description != "test tool my_tool" {
		t.Errorf("unexpected description: %q", got.Description)
	}
}

func TestRegisterToolDuplicate(t *testing.T) {
	r := NewRegistry(nil)

	if err := r.RegisterTool(newTestTool("dup")); err != nil {
		t.Fatalf("first register: %v", err)
	}

	err := r.RegisterTool(newTestTool("dup"))
	if err == nil {
		t.Fatal("expected error for duplicate tool name")
	}
	if want := `duplicate tool name: "dup"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRegisterToolReservedName(t *testing.T) {
	r := NewRegistry([]string{"read", "write", "execute"})

	err := r.RegisterTool(newTestTool("read"))
	if err == nil {
		t.Fatal("expected error for reserved tool name")
	}
	if want := `tool name "read" is reserved (built-in)`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRegisterHookAndRun(t *testing.T) {
	r := NewRegistry(nil)

	var received any
	r.RegisterHook(EventBeforeToolCall, func(_ context.Context, event any) error {
		received = event
		return nil
	})

	type testEvent struct{ Name string }
	data := testEvent{Name: "grep"}
	err := r.RunHooks(context.Background(), string(EventBeforeToolCall), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := received.(testEvent)
	if !ok {
		t.Fatalf("expected testEvent, got %T", received)
	}
	if got.Name != "grep" {
		t.Errorf("Name = %q, want %q", got.Name, "grep")
	}
}

func TestRunHooksBeforeStopsOnError(t *testing.T) {
	r := NewRegistry(nil)

	callOrder := []string{}
	r.RegisterHook(EventBeforeToolCall, func(_ context.Context, _ any) error {
		callOrder = append(callOrder, "first")
		return errors.New("blocked")
	})
	r.RegisterHook(EventBeforeToolCall, func(_ context.Context, _ any) error {
		callOrder = append(callOrder, "second")
		return nil
	})

	err := r.RunHooks(context.Background(), string(EventBeforeToolCall), nil)
	if err == nil {
		t.Fatal("expected error from before hook")
	}
	if err.Error() != "blocked" {
		t.Errorf("error = %q, want %q", err.Error(), "blocked")
	}
	if len(callOrder) != 1 || callOrder[0] != "first" {
		t.Errorf("expected only first hook to run, got %v", callOrder)
	}
}

func TestRunHooksAfterIgnoresError(t *testing.T) {
	r := NewRegistry(nil)

	r.RegisterHook(EventAfterToolCall, func(_ context.Context, _ any) error {
		return errors.New("ignored error")
	})

	err := r.RunHooks(context.Background(), string(EventAfterToolCall), nil)
	if err != nil {
		t.Fatalf("after hook error should be ignored, got: %v", err)
	}
}

func TestRunHooksNoHooks(t *testing.T) {
	r := NewRegistry(nil)

	err := r.RunHooks(context.Background(), string(EventBeforeToolCall), nil)
	if err != nil {
		t.Fatalf("expected nil error with no hooks, got: %v", err)
	}
}

func TestConcurrentRegisterAndRun(t *testing.T) {
	r := NewRegistry(nil)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n * 2)

	// Concurrently register tools.
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			_ = r.RegisterTool(newTestTool("tool_" + string(rune('a'+idx%26)) + "_" + string(rune('0'+idx/26))))
		}(i)
	}

	// Concurrently run hooks.
	r.RegisterHook(EventAfterToolCall, func(_ context.Context, _ any) error {
		return nil
	})
	for range n {
		go func() {
			defer wg.Done()
			_ = r.RunHooks(context.Background(), string(EventAfterToolCall), nil)
		}()
	}

	wg.Wait()
}
