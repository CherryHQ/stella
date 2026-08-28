package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/tools"
)

// simpleTool is a minimal tools.Tool implementation.
type simpleTool struct {
	name   string
	result string
	err    error
}

func (s *simpleTool) Definition() tools.Definition {
	return tools.Definition{Name: s.name, Description: "test tool"}
}

func (s *simpleTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return s.result, s.err
}

func newTestRegistry(t *testing.T, ts ...tools.Tool) *tools.Registry {
	t.Helper()
	r := tools.NewRegistry()
	for _, tool := range ts {
		if err := r.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Definition().Name, err)
		}
	}
	return r
}

func TestToolSetFromRegistry(t *testing.T) {
	reg := newTestRegistry(t, &simpleTool{name: "echo", result: "pong"})
	set := ToolSetFromRegistry(reg)

	if len(set) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(set))
	}

	fn, ok := set["echo"]
	if !ok {
		t.Fatal("expected 'echo' tool in set")
	}

	result, err := fn(context.Background(), ai.ToolCall{Name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if ai.FlattenText(result) != "pong" {
		t.Errorf("expected 'pong', got %q", ai.FlattenText(result))
	}
}

func TestToolSetFromRegistryFiltered_Success(t *testing.T) {
	reg := newTestRegistry(t,
		&simpleTool{name: "tool_a", result: "a"},
		&simpleTool{name: "tool_b", result: "b"},
	)

	set, defs, err := ToolSetFromRegistryFiltered(reg, []string{"tool_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 1 {
		t.Errorf("expected 1 tool in set, got %d", len(set))
	}
	if len(defs) != 1 {
		t.Errorf("expected 1 definition, got %d", len(defs))
	}
	if defs[0].Name != "tool_a" {
		t.Errorf("expected 'tool_a', got %q", defs[0].Name)
	}
	if _, ok := set["tool_b"]; ok {
		t.Error("tool_b should not be in filtered set")
	}
}

func TestToolSetFromRegistryFiltered_UnknownTool(t *testing.T) {
	reg := newTestRegistry(t, &simpleTool{name: "foo"})

	_, _, err := ToolSetFromRegistryFiltered(reg, []string{"nonexistent"})
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestWrapTool(t *testing.T) {
	tool := &simpleTool{name: "ping", result: "pong"}
	fn := WrapTool(tool)

	result, err := fn(context.Background(), ai.ToolCall{Name: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if ai.FlattenText(result) != "pong" {
		t.Errorf("expected 'pong', got %q", ai.FlattenText(result))
	}
}

func TestWrapTool_NilArgs(t *testing.T) {
	// WrapTool should handle a ToolCall with nil Arguments without panicking.
	tool := &simpleTool{name: "t", result: "ok"}
	fn := WrapTool(tool)

	result, err := fn(context.Background(), ai.ToolCall{Name: "t", Arguments: nil})
	if err != nil {
		t.Fatal(err)
	}
	if ai.FlattenText(result) != "ok" {
		t.Errorf("expected 'ok', got %q", ai.FlattenText(result))
	}
}

func TestWrapTool_Error(t *testing.T) {
	toolErr := errors.New("tool error")
	tool := &simpleTool{name: "bad", err: toolErr}
	fn := WrapTool(tool)

	_, err := fn(context.Background(), ai.ToolCall{Name: "bad"})
	if !errors.Is(err, toolErr) {
		t.Errorf("expected tool error, got %v", err)
	}
}
