package reflect

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

type fakeReflectStore struct {
	called bool
}

func (f *fakeReflectStore) ListEnabledAgents(_ context.Context) ([]config.Agent, error) {
	f.called = true
	return nil, nil
}

func (f *fakeReflectStore) Snapshot(_ context.Context, _ string) (*config.Snapshot, error) {
	return nil, nil
}

// Stubs to satisfy NewBuiltinHandler's dep validation. None of these are
// invoked when ListEnabledAgents returns no agents.

type stubMemory struct{ memory.Provider }

type stubStateStore struct{}

func (stubStateStore) Get(context.Context, pkgplugins.StateScope, string) (map[string]any, bool, error) {
	return nil, false, nil
}

func (stubStateStore) Set(context.Context, pkgplugins.StateScope, string, map[string]any) error {
	return nil
}
func (stubStateStore) Delete(context.Context, pkgplugins.StateScope, string) error { return nil }

func stubProviders(string, string, string) (providers.StreamFunc, error) { return nil, nil }

type stubPluginSkillStore struct{}

func (stubPluginSkillStore) List(context.Context, pkgplugins.SkillViewContext) ([]pkgplugins.Skill, error) {
	return nil, nil
}

func (stubPluginSkillStore) Resolve(context.Context, string, pkgplugins.SkillViewContext) (*pkgplugins.Skill, error) {
	return nil, nil
}

func (stubPluginSkillStore) ListByScope(context.Context, string, string, string) ([]pkgplugins.Skill, error) {
	return nil, nil
}

func (stubPluginSkillStore) LoadFile(context.Context, string, string) (string, error) { return "", nil }

func (stubPluginSkillStore) ListFiles(context.Context, string) ([]string, error) { return nil, nil }

func (stubPluginSkillStore) Create(context.Context, pkgplugins.Skill, map[string]string) (string, error) {
	return "", nil
}

func (stubPluginSkillStore) Update(context.Context, string, pkgplugins.SkillUpdatePatch) error {
	return nil
}

func (stubPluginSkillStore) UpsertFile(context.Context, string, string, string) error { return nil }

func (stubPluginSkillStore) DeleteFile(context.Context, string, string) error { return nil }

func (stubPluginSkillStore) Delete(context.Context, string) error { return nil }

type stubStructuredMemory struct {
	stubMemory
	memory.FactStore
	factBatchWriter
}

type stubStructuredSkillStore struct {
	stubPluginSkillStore
}

func (stubStructuredSkillStore) ListActiveReflectOwnedUserAgentSkills(context.Context, string, string) ([]pkgplugins.Skill, error) {
	return nil, nil
}

func (stubStructuredSkillStore) CreateReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillCreate) (skills.Skill, error) {
	return skills.Skill{}, nil
}

func (stubStructuredSkillStore) PatchReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillPatch) (skills.Skill, error) {
	return skills.Skill{}, nil
}

type stubArmedSkillStore struct {
	stubStructuredSkillStore
}

func (stubArmedSkillStore) DeleteReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillDelete) (skills.Skill, error) {
	return skills.Skill{}, nil
}

type dispatcherSkillAuthorizer struct{}

func (dispatcherSkillAuthorizer) AuthorizeWorkerWrite(context.Context, string, string, string, bool) error {
	return nil
}

func validConfig(store Store) Config {
	return Config{
		Memory:            stubStructuredMemory{stubMemory: stubMemory{}},
		Store:             store,
		StateStore:        stubStateStore{},
		Providers:         stubProviders,
		SkillStore:        stubArmedSkillStore{},
		SkillAuthorizer:   dispatcherSkillAuthorizer{},
		UsageCuratorStore: fakeUsageCuratorStore{},
	}
}

func TestBuiltinHandlerInvokesStore(t *testing.T) {
	store := &fakeReflectStore{}
	handler, err := NewBuiltinHandler(validConfig(store))
	if err != nil {
		t.Fatalf("NewBuiltinHandler: %v", err)
	}

	job := scheduler.Job{Name: "reflect-review"}
	if err := handler(context.Background(), job); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if !store.called {
		t.Error("ListEnabledAgents was not called")
	}
}

func TestNewBuiltinHandlerRejectsMissingDeps(t *testing.T) {
	if _, err := NewBuiltinHandler(Config{}); err == nil {
		t.Fatal("expected error for missing deps, got nil")
	}
}

func TestNewBuiltinHandlerValidatesStructuredDependencies(t *testing.T) {
	cfg := validConfig(&fakeReflectStore{})
	cfg.Memory = stubMemory{}
	if _, err := NewBuiltinHandler(cfg); err == nil {
		t.Fatal("expected missing structured memory dependencies to fail")
	}

	cfg.Memory = stubStructuredMemory{stubMemory: stubMemory{}}
	if _, err := NewBuiltinHandler(cfg); err != nil {
		t.Fatalf("structured dependencies rejected: %v", err)
	}
}

func TestNewBuiltinHandlerRejectsCuratorWithoutStore(t *testing.T) {
	for _, mode := range []UsageCuratorMode{UsageCuratorModeArmed, UsageCuratorModeShadow} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := validConfig(&fakeReflectStore{})
			cfg.UsageCuratorSettings = UsageCuratorSettings{Mode: mode}
			cfg.UsageCuratorStore = nil

			if _, err := NewBuiltinHandler(cfg); err == nil {
				t.Fatalf("expected error for %s usage curator without store", mode)
			}
		})
	}
}

func TestNewBuiltinHandlerAllowsShadowWithoutCuratorWriteDependencies(t *testing.T) {
	cfg := validConfig(&fakeReflectStore{})
	cfg.UsageCuratorSettings = UsageCuratorSettings{Mode: UsageCuratorModeShadow}
	cfg.SkillStore = stubStructuredSkillStore{}

	if _, err := NewBuiltinHandler(cfg); err != nil {
		t.Fatalf("shadow mode rejected without curator write dependencies: %v", err)
	}
}

func TestNewBuiltinHandlerRejectsArmedCuratorWithoutSkillWriter(t *testing.T) {
	cfg := validConfig(&fakeReflectStore{})
	cfg.SkillStore = stubStructuredSkillStore{}

	if _, err := NewBuiltinHandler(cfg); err == nil {
		t.Fatal("expected error for armed usage curator without skill writer")
	}
}
