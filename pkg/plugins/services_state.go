package plugins

import "context"

const (
	StateScopeGlobal  = "global"
	StateScopeUser    = "user"
	StateScopeAgent   = "agent"
	StateScopeSession = "session"
)

// StateScope identifies the owner of a stored plugin state entry.
type StateScope struct {
	Kind string
	ID   string
}

// Normalize returns the canonical scope shape used by the host store.
func (s StateScope) Normalize() StateScope {
	if s.Kind == "" {
		return StateScope{Kind: StateScopeGlobal}
	}
	if s.Kind == StateScopeGlobal {
		return StateScope{Kind: StateScopeGlobal}
	}
	return s
}

// StateStore exposes host-owned plugin persistence without leaking DB packages.
type StateStore interface {
	Get(ctx context.Context, scope StateScope, key string) (map[string]any, bool, error)
	Set(ctx context.Context, scope StateScope, key string, value map[string]any) error
	Delete(ctx context.Context, scope StateScope, key string) error
}

// PluginStateStore is the legacy unscoped plugin persistence interface retained for migration.
type PluginStateStore interface {
	Get(ctx context.Context, pluginID string, scope StateScope, key string) (map[string]any, bool, error)
	Set(ctx context.Context, pluginID string, scope StateScope, key string, value map[string]any) error
	Delete(ctx context.Context, pluginID string, scope StateScope, key string) error
}
