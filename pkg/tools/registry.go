package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// closeableTool is an optional interface for tools that need cleanup.
type closeableTool interface {
	Close() error
}

// Registry holds named tools and provides lookup + definitions.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a registry with the four core built-in tools
// (read, bash, edit, write) constructed directly as Go values.
// Optional tools like webfetch are injected externally via ExtraTools.
func NewRegistry(workDir string, userDataDir ...string) *Registry {
	if err := embedded.EnsureTools(config.AnnaHome()); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}

	var sandbox string
	if len(userDataDir) > 0 {
		sandbox = userDataDir[0]
	}
	bashDir := workDir
	if sandbox != "" {
		bashDir = sandbox
	}

	r := &Registry{tools: make(map[string]Tool)}
	r.Register(WrapWithSandbox(&ReadTool{}, sandbox, "file_path"))
	r.Register(NewBashTool(bashDir))
	r.Register(WrapWithSandbox(&EditTool{}, sandbox, "file_path"))
	r.Register(WrapWithSandbox(&WriteTool{}, sandbox, "file_path"))
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
func (r *Registry) Definitions() []Definition {
	defs := make([]Definition, 0, len(r.tools))
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
