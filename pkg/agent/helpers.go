package agent

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ToolSetFromRegistry builds a ToolSet from a tools.Registry.
// Every registered tool is included. The returned ToolSet dispatches
// calls through the registry's Execute method.
func ToolSetFromRegistry(reg *tools.Registry) ToolSet {
	set := ToolSet{}
	for _, def := range reg.Definitions() {
		name := def.Name
		set[name] = func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			return reg.ExecuteContent(ctx, name, call.Arguments)
		}
	}
	return set
}

// ToolSetFromRegistryFiltered builds a ToolSet including only the named tools.
// Returns the matching definitions and an error if any name is not found.
func ToolSetFromRegistryFiltered(reg *tools.Registry, names []string) (ToolSet, []tools.Definition, error) {
	allowed := make(map[string]bool, len(names))
	for _, n := range names {
		if !reg.Has(n) {
			return nil, nil, fmt.Errorf("unknown tool: %q", n)
		}
		allowed[n] = true
	}

	set := ToolSet{}
	var defs []tools.Definition
	for _, def := range reg.Definitions() {
		if !allowed[def.Name] {
			continue
		}
		defs = append(defs, def)
		name := def.Name
		set[name] = func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
			return reg.ExecuteContent(ctx, name, call.Arguments)
		}
	}
	return set, defs, nil
}

// WrapTool adapts a single tools.Tool into a ToolFunc.
func WrapTool(t tools.Tool) ToolFunc {
	return func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
		args := call.Arguments
		if args == nil {
			args = make(map[string]any)
		}
		return tools.ExecuteToolContent(ctx, t, args)
	}
}
