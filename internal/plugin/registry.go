package plugin

import (
	"context"
	"fmt"
	"sync"

	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

// Registry collects tools and hooks from all plugins.
type Registry struct {
	mu       sync.RWMutex
	tools    map[string]pluginapi.Tool
	hooks    map[pluginapi.EventKind][]pluginapi.HookFunc
	reserved map[string]struct{}
}

// NewRegistry creates a registry with the given built-in tool names reserved.
func NewRegistry(builtinNames []string) *Registry {
	reserved := make(map[string]struct{}, len(builtinNames))
	for _, name := range builtinNames {
		reserved[name] = struct{}{}
	}
	return &Registry{
		tools:    make(map[string]pluginapi.Tool),
		hooks:    make(map[pluginapi.EventKind][]pluginapi.HookFunc),
		reserved: reserved,
	}
}

// RegisterTool adds a tool. Returns error on duplicate or reserved name.
func (r *Registry) RegisterTool(t pluginapi.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.reserved[t.Name]; ok {
		return fmt.Errorf("tool name %q is reserved (built-in)", t.Name)
	}
	if _, ok := r.tools[t.Name]; ok {
		return fmt.Errorf("duplicate tool name: %q", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// RegisterHook adds a lifecycle hook for the given event kind.
func (r *Registry) RegisterHook(event pluginapi.EventKind, fn pluginapi.HookFunc) {
	r.mu.Lock()
	r.hooks[event] = append(r.hooks[event], fn)
	r.mu.Unlock()
}

// Tools returns a copy of all registered tools.
func (r *Registry) Tools() map[string]pluginapi.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]pluginapi.Tool, len(r.tools))
	for k, v := range r.tools {
		result[k] = v
	}
	return result
}

// RunHooks executes all hooks for the given event kind sequentially.
// For "before" events, the first hook that returns an error stops execution
// and the error is returned (cancelling the action).
// For "after" events, errors are ignored.
//
// The event parameter is a string (not EventKind) so that callers in the
// engine package can use this method without importing pkg/plugin directly,
// avoiding circular imports.
func (r *Registry) RunHooks(ctx context.Context, event string, data any) error {
	kind := pluginapi.EventKind(event)
	r.mu.RLock()
	hooks := make([]pluginapi.HookFunc, len(r.hooks[kind]))
	copy(hooks, r.hooks[kind])
	r.mu.RUnlock()

	isBefore := kind == pluginapi.EventBeforeToolCall || kind == pluginapi.EventSessionStart
	for _, fn := range hooks {
		if err := fn(ctx, data); err != nil && isBefore {
			return err
		}
	}
	return nil
}
