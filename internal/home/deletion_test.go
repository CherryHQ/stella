package home

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type (
	testFence struct {
		acquired int
		lease    *testFenceLease
	}
	testFenceLease struct {
		committed bool
		released  bool
	}
)

func (f *testFence) AcquireHomeOwnerFence(context.Context, OwnerKind, string) (OwnerFenceLease, error) {
	f.acquired++
	f.lease = &testFenceLease{}
	return f.lease, nil
}
func (l *testFenceLease) Commit()  { l.committed = true }
func (l *testFenceLease) Release() { l.released = true }

func TestOwnerDeletionFencesBeforeDBAndPreservesWorkspaceBytes(t *testing.T) {
	ctx, db, base := t.Context(), dbtest.New(t), t.TempDir()
	user, group, agentID := uuid.NewString(), uuid.NewString(), "delete-agent"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user(id,email) VALUES($1,$2)`, user, user+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(db).CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: group, Platform: "test", PlatformGroupID: group, GroupName: "group"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent (id,name,workspace) VALUES ($1,'Agent','')`, agentID); err != nil {
		t.Fatal(err)
	}
	m, err := NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	fence := &testFence{}
	d, err := NewOwnerDeletion(db, m, fence)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		req  WorkspaceRequest
		del  func() error
	}{
		{name: "user", req: WorkspaceRequest{UserID: user, AgentID: agentID}, del: func() error { return d.DeleteUser(ctx, user, "actor") }},
		{name: "group", req: WorkspaceRequest{GroupID: group, AgentID: agentID}, del: func() error { return d.DeleteGroup(ctx, group, "actor") }},
		{name: "agent", req: WorkspaceRequest{AgentID: agentID}, del: func() error { return d.DeleteAgent(ctx, agentID, "actor") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view, err := m.WorkspaceView(ctx, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			marker := view.AgentRoot + "/retained"
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(marker)
			if err != nil {
				t.Fatal(err)
			}
			priorFences := fence.acquired
			if err := tc.del(); err != nil {
				t.Fatal(err)
			}
			if fence.acquired != priorFences+1 {
				t.Fatal("owner deletion did not acquire lifecycle fence")
			}
			if fence.lease == nil || !fence.lease.committed || !fence.lease.released {
				t.Fatalf("successful fence lease = %#v", fence.lease)
			}
			if _, err := m.WorkspaceView(ctx, tc.req); err == nil {
				t.Fatal("post-delete WorkspaceView succeeded")
			}
			after, err := os.Stat(marker)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("workspace inode was not retained: %v", err)
			}
		})
	}
}

func TestOwnerDeletionReconcilesUnknownCommitOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		commit        bool
		reconcileErr  error
		wantErr       bool
		wantCommitted bool
		wantOwner     bool
	}{
		{name: "committed acknowledgement lost", commit: true, wantCommitted: true},
		{name: "transaction remains uncommitted", wantErr: true, wantOwner: true},
		{name: "reconciliation fails closed", reconcileErr: errors.New("reconcile unavailable"), wantErr: true, wantCommitted: true, wantOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db := t.Context(), dbtest.New(t)
			id := uuid.NewString()
			if _, err := sqlc.New(db).CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: id, Platform: "test", PlatformGroupID: id, GroupName: "group"}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewWorkspaceManager(db, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close() })
			fence := &testFence{}
			deletion, err := NewOwnerDeletion(db, manager, fence)
			if err != nil {
				t.Fatal(err)
			}
			deletion.commitTx = func(ctx context.Context, tx pgx.Tx) error {
				if tc.commit {
					if err := tx.Commit(ctx); err != nil {
						return err
					}
				}
				return errors.New("commit acknowledgement lost")
			}
			if tc.reconcileErr != nil {
				deletion.reconcileOwner = func(context.Context, OwnerKind, string) error { return tc.reconcileErr }
			}
			err = deletion.DeleteGroup(ctx, id, "actor")
			if (err != nil) != tc.wantErr {
				t.Fatalf("DeleteGroup error = %v, wantErr=%t", err, tc.wantErr)
			}
			if fence.lease == nil || fence.lease.committed != tc.wantCommitted || !fence.lease.released {
				t.Fatalf("fence lease = %#v, want committed=%t and released", fence.lease, tc.wantCommitted)
			}
			_, ownerErr := sqlc.New(db).GetGroupStateByID(ctx, id)
			if (ownerErr == nil) != tc.wantOwner {
				t.Fatalf("owner error = %v, wantOwner=%t", ownerErr, tc.wantOwner)
			}
		})
	}
}

func TestOwnerDeletionMissingOwnerDoesNotFence(t *testing.T) {
	db, f := dbtest.New(t), &testFence{}
	m, err := NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	d, _ := NewOwnerDeletion(db, m, f)
	if err := d.DeleteAgent(t.Context(), "missing", "actor"); err == nil {
		t.Fatal("missing owner deletion succeeded")
	}
	if f.acquired != 0 {
		t.Fatal("missing owner acquired a fence")
	}
}
