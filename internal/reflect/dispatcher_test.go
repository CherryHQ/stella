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

func (f *fakeReflectStore) ListEnabledAgents(ctx context.Context) ([]Agent, error) {
	f.sawOrg = config.OrgIDFromContext(ctx)
	return nil, nil
}

func (f *fakeReflectStore) Snapshot(ctx context.Context, _ string) (*Snapshot, error) {
	_ = config.OrgIDFromContext(ctx)
	return nil, nil
}

// Stubs used by NewDispatcher's dep validation — none of these methods are
// hit when ListEnabledAgents returns no agents.

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

func TestDispatcherHandleInjectsOrgID(t *testing.T) {
	store := &fakeReflectStore{}
	d, err := NewDispatcher(DispatcherDeps{
		Memory:     stubMemory{},
		Store:      store,
		StateStore: stubStateStore{},
		Providers:  stubProviders,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	job := scheduler.Job{Name: "reflect-review", OrgID: "org-xyz"}
	if err := d.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if store.sawOrg != "org-xyz" {
		t.Errorf("ListEnabledAgents saw OrgID=%q, want %q", store.sawOrg, "org-xyz")
	}
}

func TestDispatcherHandleRejectsEmptyOrgID(t *testing.T) {
	d, err := NewDispatcher(DispatcherDeps{
		Memory:     stubMemory{},
		Store:      &fakeReflectStore{},
		StateStore: stubStateStore{},
		Providers:  stubProviders,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if err := d.Handle(context.Background(), scheduler.Job{Name: "reflect-review"}); err == nil {
		t.Fatal("expected error for empty OrgID, got nil")
	}
}

func TestNewDispatcherRejectsMissingDeps(t *testing.T) {
	if _, err := NewDispatcher(DispatcherDeps{}); err == nil {
		t.Fatal("expected error for missing deps, got nil")
	}
}
