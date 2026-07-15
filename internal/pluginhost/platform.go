package pluginhost

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// skillStoreAdapter adapts internal/skills.Store to the plugin-facing skill API.
// It also exposes selected internal lifecycle methods by structural typing when
// host services need them.
type skillStoreAdapter struct{ s skills.Store }

func (a skillStoreAdapter) List(ctx context.Context, vc pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	rows, err := a.s.List(ctx, skills.ViewContext{
		UserID:  vc.UserID,
		AgentID: vc.AgentID,
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
	})
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	s := skillToPlugin(*r)
	return &s, nil
}

func (a skillStoreAdapter) ListByScope(ctx context.Context, scope, userID, agentID string) ([]pkgplugins.Skill, error) {
	rows, err := a.s.ListByScope(ctx, scope, userID, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.Skill, len(rows))
	for i, r := range rows {
		out[i] = skillToPlugin(r)
	}
	return out, nil
}

func (a skillStoreAdapter) ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID string, agentID string) ([]pkgplugins.Skill, error) {
	rows, err := a.s.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.Skill, len(rows))
	for i, r := range rows {
		out[i] = skillToPlugin(r)
	}
	return out, nil
}

func (a skillStoreAdapter) CreateReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillCreate) (skills.Skill, error) {
	return a.s.CreateReflectOwnedUserAgentSkill(ctx, in)
}

func (a skillStoreAdapter) PatchReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillPatch) (skills.Skill, error) {
	return a.s.PatchReflectOwnedUserAgentSkill(ctx, in)
}

func (a skillStoreAdapter) DeleteReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillDelete) (skills.Skill, error) {
	return a.s.DeleteReflectOwnedUserAgentSkill(ctx, in)
}

func (a skillStoreAdapter) TouchReflectSkillRuntimeUse(ctx context.Context, skillID string, userID string, agentID string) error {
	return a.s.TouchReflectSkillRuntimeUse(ctx, skillID, userID, agentID)
}

func (a skillStoreAdapter) LoadFile(ctx context.Context, skillID, path string) (string, error) {
	return a.s.LoadFile(ctx, skillID, path)
}

func (a skillStoreAdapter) ListFiles(ctx context.Context, skillID string) ([]string, error) {
	return a.s.ListFiles(ctx, skillID)
}

func (a skillStoreAdapter) Create(ctx context.Context, s pkgplugins.Skill, files map[string]string) (string, error) {
	return a.s.Create(ctx, skillFromPlugin(s), files)
}

func (a skillStoreAdapter) Update(ctx context.Context, id string, patch pkgplugins.SkillUpdatePatch) error {
	vc, err := a.viewContextForSkill(ctx, id)
	if err != nil {
		return err
	}
	return a.s.Update(ctx, id, vc, skills.UpdatePatch{
		Description:            patch.Description,
		Status:                 patch.Status,
		DisableModelInvocation: patch.DisableModelInvocation,
		Metadata:               patch.Metadata,
	})
}

// ApplySkillUpgrade routes production upgrades through one lifecycle
// transaction. UpgradeInStore discovers this capability by structural typing.
func (a skillStoreAdapter) ApplySkillUpgrade(ctx context.Context, id string, files map[string]string, deleteFiles []string, patch pkgplugins.SkillUpdatePatch) error {
	rows, err := a.s.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		_, err := a.s.UpdateManagedSkill(ctx, skills.ManagedSkillUpdate{
			ID: row.ID, UserID: row.UserID, AgentID: row.AgentID, Scope: row.Scope,
			Files: files, DeleteFiles: deleteFiles,
			Patch: skills.UpdatePatch{
				Description:            patch.Description,
				Status:                 patch.Status,
				DisableModelInvocation: patch.DisableModelInvocation,
				Metadata:               patch.Metadata,
			},
		})
		return err
	}
	return fmt.Errorf("upgrade skill %q: not found", id)
}

func (a skillStoreAdapter) UpsertFile(ctx context.Context, skillID, path, content string) error {
	return a.s.UpsertFile(ctx, skillID, path, content)
}

func (a skillStoreAdapter) DeleteFile(ctx context.Context, skillID, path string) error {
	return a.s.DeleteFile(ctx, skillID, path)
}

func (a skillStoreAdapter) Delete(ctx context.Context, id string) error {
	actorID := authz.UserIDFromContext(ctx)
	if actorID == "" {
		return fmt.Errorf("delete skill: authenticated actor is required")
	}
	rows, err := a.s.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		if row.Scope != "user" && row.Scope != "user_agent" {
			return fmt.Errorf("delete skill: scope %q is not user-owned", row.Scope)
		}
		if row.UserID != actorID {
			return fmt.Errorf("delete skill: skill is not owned by authenticated actor")
		}
		return a.s.Delete(ctx, id, skills.ViewContext{UserID: row.UserID, AgentID: row.AgentID})
	}
	return fmt.Errorf("delete skill %q: not found", id)
}

func (a skillStoreAdapter) viewContextForSkill(ctx context.Context, id string) (skills.ViewContext, error) {
	rows, err := a.s.ListAll(ctx)
	if err != nil {
		return skills.ViewContext{}, err
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		switch row.Scope {
		case "system_agent":
			return skills.ViewContext{AgentID: row.AgentID}, nil
		case "user":
			return skills.ViewContext{UserID: row.UserID}, nil
		case "user_agent":
			return skills.ViewContext{UserID: row.UserID, AgentID: row.AgentID}, nil
		default:
			return skills.ViewContext{}, nil
		}
	}
	return skills.ViewContext{}, nil
}

func (a skillStoreAdapter) ExpireDrafts(ctx context.Context, before time.Time) error {
	return a.s.ExpireDrafts(ctx, before)
}

// NewSkillStoreAdapter wraps an internal/skills.Store as a pkgplugins.SkillStore.
// Exported so that test code and CLI code can construct a typed adapter without
// depending on the unexported adapter type.
func NewSkillStoreAdapter(s skills.Store) pkgplugins.SkillStore {
	return skillStoreAdapter{s: s}
}

func skillToPlugin(r skills.Skill) pkgplugins.Skill {
	return pkgplugins.Skill{
		ID:                     r.ID,
		Scope:                  r.Scope,
		UserID:                 r.UserID,
		AgentID:                r.AgentID,
		Name:                   r.Name,
		Description:            r.Description,
		Status:                 r.Status,
		DisableModelInvocation: r.DisableModelInvocation,
		Metadata:               r.Metadata,
		CreatedAt:              r.CreatedAt,
		UpdatedAt:              r.UpdatedAt,
		Version:                r.Version,
	}
}

func skillFromPlugin(s pkgplugins.Skill) skills.Skill {
	return skills.Skill{
		ID:                     s.ID,
		Scope:                  s.Scope,
		UserID:                 s.UserID,
		AgentID:                s.AgentID,
		Name:                   s.Name,
		Description:            s.Description,
		Status:                 s.Status,
		DisableModelInvocation: s.DisableModelInvocation,
		Metadata:               s.Metadata,
		CreatedAt:              s.CreatedAt,
		UpdatedAt:              s.UpdatedAt,
		Version:                s.Version,
	}
}

func (h *Host) platform(pluginID string) pkgplugins.Platform {
	return pluginPlatform{host: h, pluginID: pluginID, granted: h.grantedCapabilities(pluginID)}
}

// grantedCapabilities returns the set of Platform capabilities declared by the
// plugin's registered metadata. A plugin with no metadata (or none declared)
// gets an empty set, so every gated accessor fails closed.
func (h *Host) grantedCapabilities(pluginID string) map[pkgplugins.Capability]struct{} {
	h.mu.RLock()
	defer h.mu.RUnlock()
	info, ok := h.metadataRegs[pluginID]
	if !ok || len(info.RequiredCapabilities) == 0 {
		return nil
	}
	set := make(map[pkgplugins.Capability]struct{}, len(info.RequiredCapabilities))
	for _, c := range info.RequiredCapabilities {
		set[c] = struct{}{}
	}
	return set
}

type pluginPlatform struct {
	host     *Host
	pluginID string
	granted  map[pkgplugins.Capability]struct{}
}

func (p pluginPlatform) has(c pkgplugins.Capability) bool {
	_, ok := p.granted[c]
	return ok
}

func (p pluginPlatform) Logger() *slog.Logger {
	if !p.has(pkgplugins.CapabilityLogger) {
		return nil
	}
	return p.host.Logger(p.pluginID)
}

func (p pluginPlatform) ConfigStore() pkgplugins.ConfigStore {
	if !p.has(pkgplugins.CapabilityConfigStore) {
		return nil
	}
	return scopedConfigStore{service: p.host.config, pluginID: p.pluginID}
}

func (p pluginPlatform) StateStore() pkgplugins.StateStore {
	if !p.has(pkgplugins.CapabilityStateStore) {
		return nil
	}
	return scopedStateStore{store: p.host.stateStore, pluginID: p.pluginID}
}

func (p pluginPlatform) Scheduler() pkgplugins.Scheduler {
	if !p.has(pkgplugins.CapabilityScheduler) {
		return nil
	}
	return scopedScheduler{scheduler: p.host.scheduler, pluginID: p.pluginID}
}

func (p pluginPlatform) Notifier() pkgplugins.Notifier {
	if !p.has(pkgplugins.CapabilityNotifier) {
		return nil
	}
	return p.host.Notifications()
}

func (p pluginPlatform) Auth() pkgplugins.Auth {
	if !p.has(pkgplugins.CapabilityAuth) {
		return nil
	}
	return p.host.Auth()
}

func (p pluginPlatform) RuntimeLookup() pkgplugins.RuntimeLookup {
	if !p.has(pkgplugins.CapabilityRuntimeLookup) {
		return nil
	}
	return p.host.Runtime()
}

func (p pluginPlatform) ChannelPlatform() pkgplugins.ChannelPlatform {
	if !p.has(pkgplugins.CapabilityChannelPlatform) {
		return nil
	}
	return p.host.ChannelRuntime()
}

func (p pluginPlatform) SkillStore() pkgplugins.SkillStore {
	if !p.has(pkgplugins.CapabilitySkillStore) {
		return nil
	}
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

// NewScopedStateStore wraps a StateStoreBackend as a pkgplugins.StateStore
// whose calls are namespaced to the given pluginID. Used by gateway wiring
// for built-in subsystems (e.g. reflect) that need a state store but don't
// run inside the plugin runtime.
func NewScopedStateStore(store StateStoreBackend, pluginID string) pkgplugins.StateStore {
	return scopedStateStore{store: store, pluginID: pluginID}
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
