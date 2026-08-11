package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingFencer struct {
	calls int
	err   error
}

func (f *recordingFencer) FenceHomeOwner(context.Context, OwnerKind, string) error {
	f.calls++
	return f.err
}

func seedGroup(t *testing.T, q *sqlc.Queries) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := q.CreateGroupState(context.Background(), sqlc.CreateGroupStateParams{ID: id, Platform: "test", PlatformGroupID: id, GroupName: "group"}); err != nil {
		t.Fatal(err)
	}
	return id
}

func keepFile(t *testing.T, store *LocalStore, home Record) (string, os.FileInfo) {
	t.Helper()
	name := filepath.Join(store.base, filepath.FromSlash(home.Locator), "keep")
	if err := os.WriteFile(name, []byte("exact bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return name, info
}

func TestOwnerDeletionFenceFailureAndSuccessNeverTouchPhysicalHomes(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	r, store := newRegistryWithDB(t, db)
	id := seedGroup(t, q)
	home, err := r.Ensure(context.Background(), Principal(GroupPrincipal, id))
	if err != nil {
		t.Fatal(err)
	}
	name, before := keepFile(t, store, home)
	fencer := &recordingFencer{err: errors.New("unavailable")}
	deletion, _ := NewOwnerDeletion(db, r, fencer)
	if err := deletion.DeleteGroup(context.Background(), id, "operator"); err == nil {
		t.Fatal("fencer failure succeeded")
	}
	if _, err := q.GetGroupStateByID(context.Background(), id); err != nil {
		t.Fatal("owner changed on fence failure")
	}
	if record, _ := r.Record(context.Background(), home.ID); record.State != StateReady {
		t.Fatalf("Home changed: %#v", record)
	}
	after, err := os.Stat(name)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("bytes/inode changed: %v", err)
	}
	fencer.err = nil
	if err := deletion.DeleteGroup(context.Background(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetGroupStateByID(context.Background(), id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("owner remains: %v", err)
	}
	if record, _ := r.Record(context.Background(), home.ID); record.State != StateTombstoned {
		t.Fatalf("Home not tombstoned: %#v", record)
	}
	after, err = os.Stat(name)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("physical Home changed: %v", err)
	}
	key := Principal(GroupPrincipal, id)
	if _, err := r.Ensure(context.Background(), key); err == nil {
		t.Fatal("post-delete Ensure admitted")
	}
	if _, err := r.Resolve(context.Background(), key, false); err == nil {
		t.Fatal("post-delete Resolve admitted")
	}
	if _, err := r.WorkspaceView(context.Background(), WorkspaceRequest{GroupID: id, UserID: "u", AgentID: "a"}); err == nil {
		t.Fatal("post-delete workspace admitted")
	}
}

func newRegistryWithDB(t *testing.T, db *pgxpool.Pool) (*Registry, *LocalStore) {
	t.Helper()
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	return r, store
}

func TestOwnerDeletionMissingOwnerIsNotFencedAndSentinelNeedsNoMaterialization(t *testing.T) {
	db := dbtest.New(t)
	r, store := newRegistryWithDB(t, db)
	f := &recordingFencer{}
	d, _ := NewOwnerDeletion(db, r, f)
	if err := d.DeleteGroup(context.Background(), uuid.NewString(), "operator"); err == nil {
		t.Fatal("missing owner deleted")
	}
	if f.calls != 0 {
		t.Fatalf("missing owner fenced %d times", f.calls)
	}
	id := seedGroup(t, sqlc.New(db))
	if err := d.DeleteGroup(context.Background(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), Principal(GroupPrincipal, id), false); err == nil {
		t.Fatal("sentinel resolved")
	}
	if entries, err := os.ReadDir(store.base); err != nil || len(entries) != 0 {
		t.Fatalf("sentinel materialized: %v %v", entries, err)
	}
}

func TestOwnerDeletionFKFailureRollsBackHomesButFenceIsAvailabilityOnly(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	r, _ := newRegistryWithDB(t, db)
	ctx := context.Background()
	userID, agentID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id,email) VALUES ($1,$2)", userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	home, _ := r.Ensure(ctx, SystemAgentSkills(agentID))
	if _, err := db.Exec(ctx, "INSERT INTO webhook (id,user_id,agent_id,name,provider,wait_timeout_seconds,max_run_timeout_seconds,token_public_id,token_hash,token_last4) VALUES ($1,$2,$3,'x','generic',60,300,'public','hash','1234')", uuid.NewString(), userID, agentID); err != nil {
		t.Fatal(err)
	}
	f := &recordingFencer{}
	d, _ := NewOwnerDeletion(db, r, f)
	err := d.DeleteAgent(ctx, agentID, "operator")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want FK failure: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("fence calls = %d", f.calls)
	} // Availability-only: DB rollback cannot undo a successful fence.
	if _, err := q.GetAgent(ctx, agentID); err != nil {
		t.Fatal("owner deleted")
	}
	if record, _ := r.Record(ctx, home.ID); record.State != StateReady {
		t.Fatalf("tombstone survived rollback: %#v", record)
	}
}
