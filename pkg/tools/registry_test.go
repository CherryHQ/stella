package tools

import (
	"context"
	"errors"
	"testing"
)

// mockTool is a simple Tool implementation for testing.
type mockTool struct {
	name   string
	result string
	err    error
	closed bool
}

func (m *mockTool) Definition() Definition {
	return Definition{Name: m.name, Description: "mock tool"}
}

func (m *mockTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return m.result, m.err
}

// mockCloseableTool also implements Close.
type mockCloseableTool struct {
	mockTool
	closeErr error
}

func (m *mockCloseableTool) Close() error {
	m.closed = true
	return m.closeErr
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	} else if len(r.tools) != 0 {
		t.Errorf("expected empty registry, got %d tools", len(r.tools))
	}
}

func TestRegistry_RegisterAndHas(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "foo"})
	if !r.Has("foo") {
		t.Error("expected registry to have 'foo'")
	}
	if r.Has("bar") {
		t.Error("expected registry to not have 'bar'")
	}
}

func TestRegistry_Definitions(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "a"})
	r.Register(&mockTool{name: "b"})
	defs := r.Definitions()
	if len(defs) != 2 {
		t.Errorf("expected 2 definitions, got %d", len(defs))
	}
}

func TestRegistry_BuiltinNames(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "x"})
	r.Register(&mockTool{name: "y"})
	names := r.BuiltinNames()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d", len(names))
	}
}

func TestRegistry_Execute(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "greet", result: "hello"})

	result, err := r.Execute(context.Background(), "greet", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestRegistry_Execute_UnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestRegistry_Execute_ToolError(t *testing.T) {
	toolErr := errors.New("tool failed")
	r := NewRegistry()
	r.Register(&mockTool{name: "broken", err: toolErr})

	_, err := r.Execute(context.Background(), "broken", nil)
	if !errors.Is(err, toolErr) {
		t.Errorf("expected tool error, got %v", err)
	}
}

func TestRegistry_Close(t *testing.T) {
	r := NewRegistry()
	ct := &mockCloseableTool{mockTool: mockTool{name: "closeable"}}
	r.Register(ct)
	r.Register(&mockTool{name: "plain"})

	if err := r.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ct.closed {
		t.Error("closeable tool should have been closed")
	}
}

func TestRegistry_Close_Error(t *testing.T) {
	r := NewRegistry()
	closeErr := errors.New("close error")
	ct := &mockCloseableTool{mockTool: mockTool{name: "ct"}, closeErr: closeErr}
	r.Register(ct)

	err := r.Close()
	if !errors.Is(err, closeErr) {
		t.Errorf("expected close error, got %v", err)
	}
}
