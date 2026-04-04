package tool

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
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

type closeableTool interface {
	Close() error
}

// NewRegistry creates a registry with the default built-in tools.
// Built-ins are exposed through subprocess plugin wrappers so the runner can
// exercise the same protocol path as later third-party plugins.
func NewRegistry(workDir string, userDataDir ...string) *Registry {
	reg, err := NewRegistryWithBindings(workDir, config.DefaultRuntimePluginBindings(), userDataDir...)
	if err == nil && reg != nil {
		return reg
	}
	slog.Warn("failed to configure runtime plugin bindings, falling back to builtin tool bindings", "error", err)

	var sandbox string
	if len(userDataDir) > 0 {
		sandbox = userDataDir[0]
	}
	fallback := &Registry{tools: make(map[string]Tool)}
	for _, name := range BuiltinToolNames() {
		def, _, toolErr := BuiltinToolPlugin(name, workDir, sandbox)
		if toolErr != nil {
			slog.Warn("failed to configure fallback builtin tool plugin", "tool", name, "error", toolErr)
			continue
		}
		t := newPluginTool(def)
		switch name {
		case "read", "edit", "write":
			fallback.Register(wrapWithSandbox(t, sandbox, "file_path"))
		default:
			fallback.Register(t)
		}
	}
	return fallback
}

func NewRegistryWithBindings(workDir string, bindings config.RuntimePluginBindings, userDataDir ...string) (*Registry, error) {
	if err := embedded.EnsureTools(config.AnnaHome()); err != nil {
		slog.Warn("failed to extract embedded tools", "error", err)
	}
	if err := embedded.EnsurePlugins(config.AnnaHome()); err != nil {
		slog.Warn("failed to extract embedded plugins", "error", err)
	}

	var sandbox string
	if len(userDataDir) > 0 {
		sandbox = userDataDir[0]
	}

	catalog, err := LoadCatalog(workDir, sandbox)
	if err != nil {
		return nil, err
	}

	r := &Registry{tools: make(map[string]Tool)}
	for _, name := range BuiltinToolNames() {
		pluginID := bindings.ToolBinding(name)
		def, ok := catalog.Get(pluginID)
		if !ok {
			slog.Warn("tool binding not found in runtime plugin catalog", "tool", name, "plugin", pluginID)
			continue
		}
		if def.Manifest.Kind != pluginapi.KindTool {
			return nil, fmt.Errorf("tool %s bound to non-tool plugin %s", name, pluginID)
		}
		if def.Manifest.Tool == nil || def.Manifest.Tool.Name != name {
			return nil, fmt.Errorf("tool %s bound to plugin %s exposing tool %q", name, pluginID, toolName(def))
		}

		t := newPluginTool(def)
		switch name {
		case "read", "edit", "write":
			r.Register(wrapWithSandbox(t, sandbox, "file_path"))
		default:
			r.Register(t)
		}
	}
	return r, nil
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

func LoadCatalog(workDir, userDataDir string) (*pluginhost.Catalog, error) {
	roots := []string{}
	for _, root := range []string{config.BundledPluginsPath(), config.InstalledPluginsPath()} {
		if _, err := os.Stat(root); err == nil {
			roots = append(roots, root)
		}
	}

	catalog, err := pluginhost.Discover(roots...)
	if err != nil {
		return nil, err
	}
	if err := catalog.Merge(BuiltinToolDefinitions(workDir, userDataDir)...); err != nil {
		return nil, err
	}
	return catalog, nil
}

func toolName(def pluginhost.Definition) string {
	if def.Manifest.Tool == nil {
		return ""
	}
	return def.Manifest.Tool.Name
}
