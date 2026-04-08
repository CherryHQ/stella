package plugins

import "context"

const (
	PluginStateScopeGlobal  = "global"
	PluginStateScopeUser    = "user"
	PluginStateScopeAgent   = "agent"
	PluginStateScopeSession = "session"
)

// PluginStateScope identifies the owner of a stored plugin state entry.
type PluginStateScope struct {
	Kind string
	ID   string
}

// Normalize returns the canonical scope shape used by the host store.
func (s PluginStateScope) Normalize() PluginStateScope {
	if s.Kind == "" {
		return PluginStateScope{Kind: PluginStateScopeGlobal}
	}
	if s.Kind == PluginStateScopeGlobal {
		return PluginStateScope{Kind: PluginStateScopeGlobal}
	}
	return s
}

// PluginStateStore exposes host-owned plugin persistence without leaking DB packages.
type PluginStateStore interface {
	Get(ctx context.Context, pluginID string, scope PluginStateScope, key string) (map[string]any, bool, error)
	Set(ctx context.Context, pluginID string, scope PluginStateScope, key string, value map[string]any) error
	Delete(ctx context.Context, pluginID string, scope PluginStateScope, key string) error
}
