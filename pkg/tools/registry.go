package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/pkg/ai"
)

// closeableTool is an optional interface for tools that need cleanup.
type closeableTool interface {
	Close() error
}

// Registry holds named tools and provides lookup + definitions.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
// Tools are registered externally via Register().
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// BuiltinNames returns the names of all currently registered tools.
func (r *Registry) BuiltinNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Register adds a tool to the registry. A name already taken is refused rather
// than overwritten: silently replacing a tool changes which implementation (and
// which sandbox policy) a model call reaches, and the caller that assembled the
// registry is the only place that knows which source should win.
func (r *Registry) Register(t Tool) error {
	name := t.Definition().Name
	if _, taken := r.tools[name]; taken {
		return fmt.Errorf("tools: tool %q is already registered", name)
	}
	r.tools[name] = t
	return nil
}

// Definitions returns all tool definitions in stable name order for passing to the LLM.
func (r *Registry) Definitions() []Definition {
	defs := make([]Definition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	slices.SortFunc(defs, func(a, b Definition) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return defs
}

// Has reports whether the registry contains a tool with the given name.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Execute runs the named tool with given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// ExecuteContent runs the named tool and returns its result as content blocks,
// preserving non-text content (e.g. images) from tools that emit it.
func (r *Registry) ExecuteContent(ctx context.Context, name string, args map[string]any) ([]ai.ContentBlock, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return ExecuteToolContent(ctx, t, args)
}

// Close shuts down any tools that expose a Close method.
func (r *Registry) Close() error {
	var lastErr error
	for _, t := range r.tools {
		if c, ok := t.(closeableTool); ok {
			if err := c.Close(); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}
