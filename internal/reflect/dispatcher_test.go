package reflect

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/scheduler"
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

func TestDispatcherHandleInjectsOrgID(t *testing.T) {
	store := &fakeReflectStore{}
	d := NewDispatcher(DispatcherDeps{
		Store: store,
	})

	job := scheduler.Job{Name: "reflect-review", OrgID: "org-xyz"}
	if err := d.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if store.sawOrg != "org-xyz" {
		t.Errorf("ListEnabledAgents saw OrgID=%q, want %q", store.sawOrg, "org-xyz")
	}
}
