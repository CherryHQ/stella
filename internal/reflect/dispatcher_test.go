package reflect

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

type fakeReflectStore struct {
	sawOrg string
}

func (f *fakeReflectStore) ListEnabledAgents(ctx context.Context) ([]config.Agent, error) {
	f.sawOrg = config.OrgIDFromContext(ctx)
	return nil, nil
}

func (f *fakeReflectStore) Snapshot(ctx context.Context, _ string) (*config.Snapshot, error) {
	_ = config.OrgIDFromContext(ctx)
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

func validConfig(store Store) Config {
	return Config{
		Memory:     stubMemory{},
		Store:      store,
		StateStore: stubStateStore{},
		Providers:  stubProviders,
	}
}

func TestBuiltinHandlerInjectsOrgID(t *testing.T) {
	store := &fakeReflectStore{}
	handler, err := NewBuiltinHandler(validConfig(store))
	if err != nil {
		t.Fatalf("NewBuiltinHandler: %v", err)
	}

	job := scheduler.Job{Name: "reflect-review", OrgID: "org-xyz"}
	if err := handler(context.Background(), job); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if store.sawOrg != "org-xyz" {
		t.Errorf("ListEnabledAgents saw OrgID=%q, want %q", store.sawOrg, "org-xyz")
	}
}

func TestBuiltinHandlerRejectsEmptyOrgID(t *testing.T) {
	handler, err := NewBuiltinHandler(validConfig(&fakeReflectStore{}))
	if err != nil {
		t.Fatalf("NewBuiltinHandler: %v", err)
	}
	if err := handler(context.Background(), scheduler.Job{Name: "reflect-review"}); err == nil {
		t.Fatal("expected error for empty OrgID, got nil")
	}
}

func TestNewBuiltinHandlerRejectsMissingDeps(t *testing.T) {
	if _, err := NewBuiltinHandler(Config{}); err == nil {
		t.Fatal("expected error for missing deps, got nil")
	}
}
