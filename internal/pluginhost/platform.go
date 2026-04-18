package pluginhost

import (
	"context"
	"log/slog"

	"github.com/vaayne/anna/internal/skills"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// skillStoreAdapter adapts internal/skills.Store to pkg/plugins.SkillStore.
// Both interfaces have identical shapes but use locally-defined types, so a
// thin adapter is needed to bridge the two packages without a circular import.
type skillStoreAdapter struct{ s skills.Store }

func (a skillStoreAdapter) List(ctx context.Context, vc pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	rows, err := a.s.List(ctx, skills.ViewContext{
		UserID:  vc.UserID,
		AgentID: vc.AgentID,
		Project: vc.Project,
	})
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.Skill, len(rows))
	for i, r := range rows {
		out[i] = skillToPlugin(r)
	}
	return out, nil
}

func (a skillStoreAdapter) Resolve(ctx context.Context, name string, vc pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	r, err := a.s.Resolve(ctx, name, skills.ViewContext{
		UserID:  vc.UserID,
		AgentID: vc.AgentID,
		Project: vc.Project,
	})
	if err != nil {
		return nil, err
	}
	s := skillToPlugin(*r)
	return &s, nil
}

func (a skillStoreAdapter) LoadFile(ctx context.Context, skillID, path string) (string, error) {
	return a.s.LoadFile(ctx, skillID, path)
}

func (a skillStoreAdapter) Create(ctx context.Context, s pkgplugins.Skill, files map[string]string) (string, error) {
	return a.s.Create(ctx, skillFromPlugin(s), files)
}

func (a skillStoreAdapter) Update(ctx context.Context, id string, patch pkgplugins.SkillUpdatePatch) error {
	return a.s.Update(ctx, id, skills.UpdatePatch{
		Description:            patch.Description,
		Status:                 patch.Status,
		DisableModelInvocation: patch.DisableModelInvocation,
		Metadata:               patch.Metadata,
	})
}

func (a skillStoreAdapter) UpsertFile(ctx context.Context, skillID, path, content string) error {
	return a.s.UpsertFile(ctx, skillID, path, content)
}

func (a skillStoreAdapter) DeleteFile(ctx context.Context, skillID, path string) error {
	return a.s.DeleteFile(ctx, skillID, path)
}

func (a skillStoreAdapter) Delete(ctx context.Context, id string) error {
	return a.s.Delete(ctx, id)
}

func skillToPlugin(r skills.Skill) pkgplugins.Skill {
	return pkgplugins.Skill{
		ID:                     r.ID,
		Scope:                  r.Scope,
		UserID:                 r.UserID,
		AgentID:                r.AgentID,
		Project:                r.Project,
		Name:                   r.Name,
		Description:            r.Description,
		Status:                 r.Status,
		DisableModelInvocation: r.DisableModelInvocation,
		Metadata:               r.Metadata,
		CreatedAt:              r.CreatedAt,
		UpdatedAt:              r.UpdatedAt,
	}
}

func skillFromPlugin(s pkgplugins.Skill) skills.Skill {
	return skills.Skill{
		ID:                     s.ID,
		Scope:                  s.Scope,
		UserID:                 s.UserID,
		AgentID:                s.AgentID,
		Project:                s.Project,
		Name:                   s.Name,
		Description:            s.Description,
		Status:                 s.Status,
		DisableModelInvocation: s.DisableModelInvocation,
		Metadata:               s.Metadata,
		CreatedAt:              s.CreatedAt,
		UpdatedAt:              s.UpdatedAt,
	}
}

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

func (p pluginPlatform) SkillStore() pkgplugins.SkillStore {
	s := p.host.SkillStore()
	if s == nil {
		return nil
	}
	return skillStoreAdapter{s: s}
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
