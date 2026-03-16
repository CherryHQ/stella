package tool

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/toolspec"
)

// Tool is a built-in tool that can be executed by the Go runner.
type Tool interface {
	Definition() toolspec.Definition
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// Registry holds named tools and provides lookup + definitions.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a registry with the default built-in tools.
func NewRegistry(workDir string) *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	r.Register(&ReadTool{})
	r.Register(&BashTool{workDir: workDir})
	r.Register(&EditTool{})
	r.Register(&WriteTool{})
	r.Register(NewWebFetchTool())
	return r
}

// BuiltinNames returns the names of all currently registered tools.
func (r *Registry) BuiltinNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Definition().Name] = t
}

// Definitions returns all tool definitions for passing to the LLM.
func (r *Registry) Definitions() []toolspec.Definition {
	defs := make([]toolspec.Definition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
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
