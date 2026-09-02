package host

import (
	"context"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// ConfigBackend is the host-owned unscoped plugin config backend.
type ConfigBackend interface {
	Get(ctx context.Context, pluginID string) (pkgplugins.PluginState, error)
	Set(ctx context.Context, pluginID string, config map[string]any) error
	SetEnabled(ctx context.Context, pluginID string, enabled bool) error
}

// StateStoreBackend is the host-owned unscoped plugin state backend.
type StateStoreBackend interface {
	Get(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) (map[string]any, bool, error)
	Set(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string, value map[string]any) error
	Delete(ctx context.Context, pluginID string, scope pkgplugins.StateScope, key string) error
}
