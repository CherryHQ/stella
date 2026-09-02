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

// SchedulerBackend is the host-owned unscoped scheduler backend.
type SchedulerBackend interface {
	ReconcilePluginJobs(ctx context.Context, pluginID string, jobs []pkgplugins.SchedulerJobSpec) error
	DeletePluginJobs(ctx context.Context, pluginID string) error
	DeletePluginJob(ctx context.Context, pluginID string, key string) error
	ListPluginJobs(ctx context.Context, pluginID string) ([]pkgplugins.SchedulerJob, error)
}
