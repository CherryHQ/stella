package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func mustRegister(t *testing.T, r *Registry, tool Tool) {
	t.Helper()
	if err := r.Register(tool); err != nil {
		t.Fatalf("register %s: %v", tool.Definition().Name, err)
	}
}

func TestRegistry_RegisterAndHas(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &mockTool{name: "foo"})
	if !r.Has("foo") {
		t.Error("expected registry to have 'foo'")
	}
	if r.Has("bar") {
		t.Error("expected registry to not have 'bar'")
	}
}

func TestRegistry_DefinitionsStableOrder(t *testing.T) {
	registrationOrders := [][]string{
		{"zeta", "alpha", "middle"},
		{"middle", "zeta", "alpha"},
	}
	wantNames := []string{"alpha", "middle", "zeta"}
	var wantJSON string

	for _, order := range registrationOrders {
		r := NewRegistry()
		for _, name := range order {
			mustRegister(t, r, &mockTool{name: name})
		}

		// Materialize repeatedly so map iteration can never leak into provider-facing order.
		for range 16 {
			defs := r.Definitions()
			if len(defs) != len(wantNames) {
				t.Fatalf("Definitions() length = %d, want %d", len(defs), len(wantNames))
			}
			for i, wantName := range wantNames {
				if defs[i].Name != wantName {
					t.Fatalf("Definitions()[%d].Name = %q, want %q; definitions = %#v", i, defs[i].Name, wantName, defs)
				}
			}

			encoded, err := json.Marshal(defs)
			if err != nil {
				t.Fatalf("json.Marshal(Definitions()) error: %v", err)
			}
			if wantJSON == "" {
				wantJSON = string(encoded)
			} else if string(encoded) != wantJSON {
				t.Fatalf("serialized Definitions() = %s, want %s", encoded, wantJSON)
			}
		}
	}
}

func TestRegistry_BuiltinNames(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &mockTool{name: "x"})
	mustRegister(t, r, &mockTool{name: "y"})
	names := r.BuiltinNames()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d", len(names))
	}
}

func TestRegistry_Execute(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &mockTool{name: "greet", result: "hello"})

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
	mustRegister(t, r, &mockTool{name: "broken", err: toolErr})

	_, err := r.Execute(context.Background(), "broken", nil)
	if !errors.Is(err, toolErr) {
		t.Errorf("expected tool error, got %v", err)
	}
}

func TestRegistry_Close(t *testing.T) {
	r := NewRegistry()
	ct := &mockCloseableTool{mockTool: mockTool{name: "closeable"}}
	mustRegister(t, r, ct)
	mustRegister(t, r, &mockTool{name: "plain"})

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
	mustRegister(t, r, ct)

	err := r.Close()
	if !errors.Is(err, closeErr) {
		t.Errorf("expected close error, got %v", err)
	}
}

// A second tool answering to a live name is refused, and the tool already in the
// registry keeps the name: a silent swap would move calls to a different
// implementation without any caller noticing.
func TestRegistry_RegisterRejectsDuplicateName(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &mockTool{name: "share", result: "first"})

	err := r.Register(&mockTool{name: "share", result: "second"})
	if err == nil {
		t.Fatal("expected duplicate registration to be refused")
	}
	if !strings.Contains(err.Error(), "share") {
		t.Fatalf("error should name the tool, got %v", err)
	}
	result, execErr := r.Execute(context.Background(), "share", nil)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	if result != "first" {
		t.Fatalf("incumbent tool should keep the name, got %q", result)
	}
}
