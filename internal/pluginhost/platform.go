package pluginhost

import (
	"context"
	"log/slog"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func (h *Host) platform(pluginID string) pkgplugins.Platform {
	return pluginPlatform{host: h, pluginID: pluginID}
}

type pluginPlatform struct {
	host     *Host
	pluginID string
}

func (p pluginPlatform) Logger() *slog.Logger { return p.host.Logger(p.pluginID) }
func (p pluginPlatform) ConfigStore() pkgplugins.ConfigStore {
	return scopedConfigStore{service: p.host.config, pluginID: p.pluginID}
}

func (p pluginPlatform) StateStore() pkgplugins.StateStore {
	return scopedStateStore{store: p.host.stateStore, pluginID: p.pluginID}
}

func (p pluginPlatform) Scheduler() pkgplugins.Scheduler {
	return scopedScheduler{scheduler: p.host.scheduler, pluginID: p.pluginID}
}
func (p pluginPlatform) Notifier() pkgplugins.Notifier           { return p.host.Notifications() }
func (p pluginPlatform) Auth() pkgplugins.Auth                   { return p.host.Auth() }
func (p pluginPlatform) RuntimeLookup() pkgplugins.RuntimeLookup { return p.host.Runtime() }
func (p pluginPlatform) ChannelPlatform() pkgplugins.ChannelPlatform {
	return p.host.ChannelRuntime()
}

func (p pluginPlatform) ReflectPlatform() pkgplugins.ReflectPlatform {
	return p.host.ReflectRuntime()
}

type scopedConfigStore struct {
	service  ConfigBackend
	pluginID string
}

func (s scopedConfigStore) Get(ctx context.Context) (pkgplugins.PluginState, error) {
	return s.service.Get(ctx, s.pluginID)
}

func (s scopedConfigStore) Set(ctx context.Context, config map[string]any) error {
	return s.service.Set(ctx, s.pluginID, config)
}

type scopedStateStore struct {
	store    StateStoreBackend
	pluginID string
}

func (s scopedStateStore) Get(ctx context.Context, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
	if s.store == nil {
		return nil, false, nil
	}
	return s.store.Get(ctx, s.pluginID, scope, key)
}

func (s scopedStateStore) Set(ctx context.Context, scope pkgplugins.StateScope, key string, value map[string]any) error {
	if s.store == nil {
		return nil
	}
	return s.store.Set(ctx, s.pluginID, scope, key, value)
}

func (s scopedStateStore) Delete(ctx context.Context, scope pkgplugins.StateScope, key string) error {
	if s.store == nil {
		return nil
	}
	return s.store.Delete(ctx, s.pluginID, scope, key)
}

type scopedScheduler struct {
	scheduler SchedulerBackend
	pluginID  string
}

func (s scopedScheduler) ReconcileJobs(ctx context.Context, jobs []pkgplugins.SchedulerJobSpec) error {
	if s.scheduler == nil {
		return nil
	}
	return s.scheduler.ReconcilePluginJobs(ctx, s.pluginID, jobs)
}

func (s scopedScheduler) DeleteJobs(ctx context.Context) error {
	if s.scheduler == nil {
		return nil
	}
	return s.scheduler.DeletePluginJobs(ctx, s.pluginID)
}

func (s scopedScheduler) DeleteJob(ctx context.Context, key string) error {
	if s.scheduler == nil {
		return nil
	}
	return s.scheduler.DeletePluginJob(ctx, s.pluginID, key)
}

func (s scopedScheduler) ListJobs(ctx context.Context) ([]pkgplugins.SchedulerJob, error) {
	if s.scheduler == nil {
		return nil, nil
	}
	return s.scheduler.ListPluginJobs(ctx, s.pluginID)
}
