package tool

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
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
// When userDataDir is non-empty, file tools (read, write, edit) are wrapped
// with sandbox validation that restricts paths to the user's data directory.
// The bash tool uses userDataDir as its working directory when set.
func NewRegistry(workDir string, userDataDir ...string) *Registry {
	if err := embedded.EnsureTools(config.AnnaHome()); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}

	var sandbox string
	if len(userDataDir) > 0 {
		sandbox = userDataDir[0]
	}

	// Use user data dir as bash work dir when available.
	bashDir := workDir
	if sandbox != "" {
		bashDir = sandbox
	}

	r := &Registry{tools: make(map[string]Tool)}
	r.Register(wrapWithSandbox(&ReadTool{}, sandbox, "file_path"))
	r.Register(&BashTool{workDir: bashDir})
	r.Register(wrapWithSandbox(&EditTool{}, sandbox, "file_path"))
	r.Register(wrapWithSandbox(&WriteTool{}, sandbox, "file_path"))
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
