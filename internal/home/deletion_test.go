package home

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingOwnerEnqueue struct{ args []ownerPurgeArgs }

func (e *recordingOwnerEnqueue) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	e.args = append(e.args, args.(ownerPurgeArgs))
	return nil, nil
}

type recordingOwnerFencer struct {
	calls int
	err   error
}

type recordingPurgeStore struct {
	Store
	order []string
	fail  map[string]error
}

func (s *recordingPurgeStore) Purge(ctx context.Context, record Record) error {
	s.order = append(s.order, record.ID)
	if err := s.fail[record.ID]; err != nil {
		return err
	}
	return s.Store.Purge(ctx, record)
}

func (f *recordingOwnerFencer) FenceHomeOwner(context.Context, OwnerKind, string) error {
	f.calls++
	return f.err
}

func TestOwnerDeletionGroupTombstonesSentinelAndChildrenAtomically(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	groupID := uuid.NewString()
	q := sqlc.New(db)
	if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: groupID, Platform: "test", PlatformGroupID: groupID, GroupName: "test", CreatedByUserID: pgtype.Text{}}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	principal, err := registry.Ensure(ctx, Principal(GroupPrincipal, groupID))
	if err != nil {
		t.Fatal(err)
	}
	child, err := registry.Ensure(ctx, Agent(GroupPrincipal, groupID, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	enqueue, fencer := &recordingOwnerEnqueue{}, &recordingOwnerFencer{}
	deletion, err := NewOwnerDeletion(db, registry, enqueue, fencer)
	if err != nil {
		t.Fatal(err)
	}
	if err := deletion.DeleteGroup(ctx, groupID, "operator"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if fencer.calls != 1 {
		t.Fatalf("synchronous fences = %d, want 1", fencer.calls)
	}
	if len(enqueue.args) != 1 || len(enqueue.args[0].HomeIDs) != 2 {
		t.Fatalf("batches = %#v, want one sentinel+child batch", enqueue.args)
	}
	for _, id := range []string{principal.ID, child.ID} {
		record, err := registry.Record(ctx, id)
		if err != nil || record.State != StateTombstoned {
			t.Fatalf("Home %s = %#v, %v; want tombstoned", id, record, err)
		}
	}
	if _, err := q.GetGroupStateByID(ctx, groupID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("group still exists: %v", err)
	}
	if _, err := registry.Resolve(ctx, Principal(GroupPrincipal, groupID), false); err == nil {
		t.Fatal("tombstoned group still attached")
	}
	if _, err := registry.WorkspaceView(ctx, WorkspaceRequest{UserID: "synthetic", GroupID: groupID, AgentID: "agent"}); err == nil {
		t.Fatal("tombstoned group still produced WorkspaceView")
	}
}

func TestOwnerDeletionAgentTombstonesUserGroupAndSystemAgentHomes(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store, _ := NewLocalStore("local", t.TempDir())
	registry, _ := NewRegistry(db, store.ID(), store)
	q := sqlc.New(db)
	agentID := uuid.NewString()
	if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	groupID := uuid.NewString()
	if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: groupID, Platform: "test", PlatformGroupID: groupID, GroupName: "group"}); err != nil {
		t.Fatal(err)
	}
	userHome, err := registry.Ensure(ctx, Agent(UserPrincipal, "user", agentID))
	if err != nil {
		t.Fatal(err)
	}
	groupHome, err := registry.Ensure(ctx, Agent(GroupPrincipal, groupID, agentID))
	if err != nil {
		t.Fatal(err)
	}
	root, err := registry.Ensure(ctx, SystemAgentSkills(agentID))
	if err != nil {
		t.Fatal(err)
	}
	enqueue := &recordingOwnerEnqueue{}
	deletion, _ := NewOwnerDeletion(db, registry, enqueue, &recordingOwnerFencer{})
	if err := deletion.DeleteAgent(ctx, agentID, "operator"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if len(enqueue.args) != 1 || len(enqueue.args[0].HomeIDs) != 3 {
		t.Fatalf("purge batch = %#v, want user+group+system agent Homes", enqueue.args)
	}
	for _, id := range []string{userHome.ID, groupHome.ID, root.ID} {
		record, err := registry.Record(ctx, id)
		if err != nil || record.State != StateTombstoned {
			t.Fatalf("Home %s = %#v, %v; want tombstoned", id, record, err)
		}
	}
	if _, err := registry.WorkspaceView(ctx, WorkspaceRequest{UserID: "user", AgentID: agentID}); err == nil {
		t.Fatal("stale agent WorkspaceView recreated an attachment")
	}
}

func TestOwnerDeletionSkipsTerminalSharedHomesForOverlappingOwners(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name   string
		first  func(*OwnerDeletion, context.Context, string, string) error
		second func(*OwnerDeletion, context.Context, string, string) error
	}{
		{name: "group then agent", first: func(d *OwnerDeletion, ctx context.Context, groupID, _ string) error {
			return d.DeleteGroup(ctx, groupID, "first")
		}, second: func(d *OwnerDeletion, ctx context.Context, _, agentID string) error {
			return d.DeleteAgent(ctx, agentID, "second")
		}},
		{name: "agent then group", first: func(d *OwnerDeletion, ctx context.Context, _, agentID string) error {
			return d.DeleteAgent(ctx, agentID, "first")
		}, second: func(d *OwnerDeletion, ctx context.Context, groupID, _ string) error {
			return d.DeleteGroup(ctx, groupID, "second")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := dbtest.New(t)
			store, err := NewLocalStore("local", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewRegistry(db, store.ID(), store)
			if err != nil {
				t.Fatal(err)
			}
			q := sqlc.New(db)
			groupID, agentID := uuid.NewString(), uuid.NewString()
			if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: groupID, Platform: "test", PlatformGroupID: groupID, GroupName: "group"}); err != nil {
				t.Fatal(err)
			}
			if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			shared, err := registry.Ensure(ctx, Agent(GroupPrincipal, groupID, agentID))
			if err != nil {
				t.Fatal(err)
			}
			enqueue := &recordingOwnerEnqueue{}
			deletion, err := NewOwnerDeletion(db, registry, enqueue, &recordingOwnerFencer{})
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.first(deletion, ctx, groupID, agentID); err != nil {
				t.Fatalf("first delete: %v", err)
			}
			prior, err := q.GetStorageHome(ctx, shared.ID)
			if err != nil || prior.State != string(StateTombstoned) {
				t.Fatalf("prior shared Home = %#v, %v", prior, err)
			}
			if err := tt.second(deletion, ctx, groupID, agentID); err != nil {
				t.Fatalf("second group delete: %v", err)
			}
			if len(enqueue.args) != 2 || len(enqueue.args[1].HomeIDs) != 1 {
				t.Fatalf("batches = %#v, want one new Home in second batch", enqueue.args)
			}
			second, err := registry.Record(ctx, enqueue.args[1].HomeIDs[0])
			if err != nil || second.State != StateTombstoned || second.ID == shared.ID {
				t.Fatalf("second batch Home = %#v, %v; want new tombstoned sentinel", second, err)
			}
			after, err := q.GetStorageHome(ctx, shared.ID)
			if err != nil || after.State != prior.State || after.TombstonedAt != prior.TombstonedAt || after.TombstonedBy != prior.TombstonedBy || after.PurgeRequestedAt != prior.PurgeRequestedAt {
				t.Fatalf("shared audit changed: before=%#v after=%#v err=%v", prior, after, err)
			}
			if _, err := q.GetGroupStateByID(ctx, groupID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("group remains: %v", err)
			}
			if _, err := q.GetAgent(ctx, agentID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("agent remains: %v", err)
			}
		})
	}
}

func TestOwnerDeletionAgentInUseRollsBackHomesAndJob(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	userID, agentID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: agentID, Name: "agent", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	root, err := registry.Ensure(ctx, SystemAgentSkills(agentID))
	if err != nil {
		t.Fatalf("ensure system Agent Home: %v", err)
	}
	// Keep this fixture aligned with internal/webhook/store_pg_integration_test.go:
	// an Agent referenced by webhook_agent_id_fkey cannot be deleted.
	if _, err := db.Exec(ctx, "INSERT INTO webhook (id, user_id, agent_id, name, provider, wait_timeout_seconds, max_run_timeout_seconds, token_public_id, token_hash, token_last4) VALUES ($1,$2,$3,'x','generic',60,300,'public','hash','1234')", uuid.NewString(), userID, agentID); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	enqueue, fencer := &recordingOwnerEnqueue{}, &recordingOwnerFencer{}
	deletion, err := NewOwnerDeletion(db, registry, enqueue, fencer)
	if err != nil {
		t.Fatal(err)
	}
	err = deletion.DeleteAgent(ctx, agentID, "operator")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23001" || pgErr.ConstraintName != "webhook_agent_id_fkey" {
		t.Fatalf("DeleteAgent error = %v, want webhook FK violation", err)
	}
	if _, err := q.GetAgent(ctx, agentID); err != nil {
		t.Fatalf("Agent was deleted: %v", err)
	}
	for _, id := range []string{root.ID} {
		record, err := registry.Record(ctx, id)
		if err != nil || record.State != StateReady {
			t.Fatalf("Home %s = %#v, %v; want ready", id, record, err)
		}
	}
	if len(enqueue.args) != 0 {
		t.Fatalf("enqueued batches = %#v, want none", enqueue.args)
	}
	if fencer.calls != 0 {
		t.Fatalf("fencer calls = %d, want 0", fencer.calls)
	}
}

func TestOwnerDeletionCreatesSentinelForNeverMaterializedOwner(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name      string
		kind      OwnerKind
		seed      func(*testing.T, *pgxpool.Pool, *sqlc.Queries, string)
		delete    func(*OwnerDeletion, context.Context, string) error
		missing   func(*sqlc.Queries, context.Context, string) error
		workspace func(*Registry, context.Context, string, string) error
		wantError string
	}{
		{
			name: "user", kind: OwnerUser,
			seed: func(t *testing.T, db *pgxpool.Pool, _ *sqlc.Queries, id string) {
				t.Helper()
				if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", id, id+"@example.test"); err != nil {
					t.Fatal(err)
				}
			},
			delete: func(d *OwnerDeletion, ctx context.Context, id string) error { return d.DeleteUser(ctx, id, "operator") },
			missing: func(q *sqlc.Queries, ctx context.Context, id string) error {
				_, err := q.GetAuthUser(ctx, id)
				return err
			},
			workspace: func(r *Registry, ctx context.Context, id, agentID string) error {
				_, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: id, AgentID: agentID})
				return err
			},
			wantError: "ensure principal workspace",
		},
		{
			name: "group", kind: OwnerGroup,
			seed: func(t *testing.T, _ *pgxpool.Pool, q *sqlc.Queries, id string) {
				t.Helper()
				if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: id, Platform: "test", PlatformGroupID: id, GroupName: "group"}); err != nil {
					t.Fatal(err)
				}
			},
			delete: func(d *OwnerDeletion, ctx context.Context, id string) error {
				return d.DeleteGroup(ctx, id, "operator")
			},
			missing: func(q *sqlc.Queries, ctx context.Context, id string) error {
				_, err := q.GetGroupStateByID(ctx, id)
				return err
			},
			workspace: func(r *Registry, ctx context.Context, id, agentID string) error {
				_, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: id, GroupID: id, AgentID: agentID})
				return err
			},
			wantError: "ensure principal workspace",
		},
		{
			name: "agent", kind: OwnerAgent,
			seed: func(t *testing.T, _ *pgxpool.Pool, q *sqlc.Queries, id string) {
				t.Helper()
				if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: id, Name: "owner", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
					t.Fatal(err)
				}
			},
			delete: func(d *OwnerDeletion, ctx context.Context, id string) error {
				return d.DeleteAgent(ctx, id, "operator")
			},
			missing: func(q *sqlc.Queries, ctx context.Context, id string) error { _, err := q.GetAgent(ctx, id); return err },
			workspace: func(r *Registry, ctx context.Context, id, principalID string) error {
				_, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: principalID, AgentID: id})
				return err
			},
			wantError: "ensure system Agent Skill root",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := dbtest.New(t)
			store, err := NewLocalStore("local", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewRegistry(db, store.ID(), store)
			if err != nil {
				t.Fatal(err)
			}
			q := sqlc.New(db)
			ownerID, validAgentID, validPrincipalID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			// User/group WorkspaceView needs an existing Agent; Agent WorkspaceView
			// needs an existing principal, so the sentinel is the failing boundary.
			if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: validAgentID, Name: "valid", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", validPrincipalID, validPrincipalID+"@example.test"); err != nil {
				t.Fatal(err)
			}
			tt.seed(t, db, q, ownerID)
			enqueue := &recordingOwnerEnqueue{}
			deletion, err := NewOwnerDeletion(db, registry, enqueue, &recordingOwnerFencer{})
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.delete(deletion, ctx, ownerID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if err := tt.missing(q, ctx, ownerID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("owner row lookup = %v, want missing", err)
			}
			if len(enqueue.args) != 1 || len(enqueue.args[0].HomeIDs) != 1 {
				t.Fatalf("purge batches = %#v, want one sentinel", enqueue.args)
			}
			sentinel, err := registry.Record(ctx, enqueue.args[0].HomeIDs[0])
			if err != nil || sentinel.State != StateTombstoned {
				t.Fatalf("sentinel = %#v, %v; want tombstoned", sentinel, err)
			}
			workspaceID := validAgentID
			if tt.kind == OwnerAgent {
				workspaceID = validPrincipalID
			}
			if err := tt.workspace(registry, ctx, ownerID, workspaceID); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("WorkspaceView error = %v, want %q sentinel rejection", err, tt.wantError)
			}
		})
	}
}

func TestOwnerDeletionImmediateFencerFailureIsLoggedAndLeavesCommittedRecovery(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store, _ := NewLocalStore("local", t.TempDir())
	registry, _ := NewRegistry(db, store.ID(), store)
	groupID := uuid.NewString()
	if _, err := sqlc.New(db).CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: groupID, Platform: "test", PlatformGroupID: groupID, GroupName: "test"}); err != nil {
		t.Fatal(err)
	}
	enqueue, fencer := &recordingOwnerEnqueue{}, &recordingOwnerFencer{err: errors.New("down")}
	deletion, _ := NewOwnerDeletion(db, registry, enqueue, fencer)
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	if err := deletion.DeleteGroup(ctx, groupID, "operator"); err != nil {
		t.Fatalf("committed deletion returned immediate fencer failure: %v", err)
	}
	for _, want := range []string{
		"immediate owner execution fence failed after deletion commit",
		`"owner_kind":"group"`,
		`"owner_id":"` + groupID + `"`,
		`"error":"down"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("fencer failure log = %s, want %q", logs.String(), want)
		}
	}
	if len(enqueue.args) != 1 {
		t.Fatal("committed deletion did not retain River recovery job")
	}
	if record, err := registry.Record(ctx, enqueue.args[0].HomeIDs[0]); err != nil || record.State != StateTombstoned {
		t.Fatalf("committed sentinel = %#v, %v", record, err)
	}
}

func TestOwnerPurgeWorkerFencesBeforePhysicalPurgeAndOrdersChildren(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	base, _ := NewLocalStore("local", t.TempDir())
	store := &recordingPurgeStore{Store: base, fail: map[string]error{}}
	registry, _ := NewRegistry(db, store.ID(), store)
	principal, err := registry.Ensure(ctx, Principal(UserPrincipal, "user"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := registry.Ensure(ctx, Agent(UserPrincipal, "user", "agent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []Key{principal.Key, child.Key} {
		if _, err := registry.Tombstone(ctx, key, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	fencer := &recordingOwnerFencer{}
	worker := &ownerPurgeWorker{deletion: &OwnerDeletion{registry: registry, fencer: fencer}}
	if err := worker.Work(ctx, &river.Job[ownerPurgeArgs]{Args: ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "user", HomeIDs: []string{principal.ID, child.ID}, Actor: "operator"}}); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(store.order) != 2 || store.order[0] != child.ID || store.order[1] != principal.ID {
		t.Fatalf("physical order = %v, want child then principal", store.order)
	}

	// A fencer outage is retryable and must leave every physical byte alone.
	third, err := registry.Ensure(ctx, Principal(UserPrincipal, "other"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Tombstone(ctx, third.Key, "operator"); err != nil {
		t.Fatal(err)
	}
	before := len(store.order)
	fencer.err = errors.New("fencer unavailable")
	err = worker.Work(ctx, &river.Job[ownerPurgeArgs]{Args: ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "other", HomeIDs: []string{third.ID}, Actor: "operator"}})
	if err == nil || len(store.order) != before {
		t.Fatalf("fencer failure = %v, physical calls = %v", err, store.order)
	}
}

func TestOwnerPurgeWorkerReturnsTransientLoadErrorAfterOtherRecords(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store, _ := NewLocalStore("local", t.TempDir())
	registry, _ := NewRegistry(db, store.ID(), store)
	good, err := registry.Ensure(ctx, Principal(UserPrincipal, "good"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Tombstone(ctx, good.Key, "operator"); err != nil {
		t.Fatal(err)
	}
	worker := &ownerPurgeWorker{deletion: &OwnerDeletion{registry: registry, fencer: &recordingOwnerFencer{}}}
	err = worker.Work(ctx, &river.Job[ownerPurgeArgs]{Args: ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "good", HomeIDs: []string{"missing", good.ID}, Actor: "operator"}})
	if err == nil {
		t.Fatal("missing record load was acknowledged")
	}
	if record, err := registry.Record(ctx, good.ID); err != nil || record.State != StateTombstoned {
		t.Fatalf("potential parent record = %#v, %v; want tombstoned", record, err)
	}
}

func TestOwnerPurgeWorkerDoesNotDeleteParentAfterChildFailure(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	base, _ := NewLocalStore("local", t.TempDir())
	store := &recordingPurgeStore{Store: base, fail: map[string]error{}}
	registry, _ := NewRegistry(db, store.ID(), store)
	principal, _ := registry.Ensure(ctx, Principal(UserPrincipal, "user-with-failed-child"))
	child, _ := registry.Ensure(ctx, Agent(UserPrincipal, "user-with-failed-child", "agent"))
	for _, key := range []Key{principal.Key, child.Key} {
		if _, err := registry.Tombstone(ctx, key, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	store.fail[child.ID] = errors.New("disk full")
	worker := &ownerPurgeWorker{deletion: &OwnerDeletion{registry: registry, fencer: &recordingOwnerFencer{}}}
	job := &river.Job[ownerPurgeArgs]{Args: ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "user-with-failed-child", HomeIDs: []string{principal.ID, child.ID}, Actor: "operator"}}
	var snooze *river.JobSnoozeError
	if err := worker.Work(ctx, job); !errors.As(err, &snooze) {
		t.Fatalf("child-blocked parent purge = %v, want durable snooze", err)
	}
	if len(store.order) != 1 || store.order[0] != child.ID {
		t.Fatalf("physical order after child failure = %v, want child only", store.order)
	}
	if record, err := registry.Record(ctx, principal.ID); err != nil || record.State != StateTombstoned {
		t.Fatalf("parent after child failure = %#v, %v", record, err)
	}
	delete(store.fail, child.ID)
	if _, err := registry.RetryFailedPurge(ctx, child.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("parent continuation after child retry: %v", err)
	}
	if record, err := registry.Record(ctx, principal.ID); err != nil || record.State != StatePurged {
		t.Fatalf("parent after child recovery = %#v, %v", record, err)
	}
}

func TestOwnerPurgeWorkerSuppressesOnlyRecordedPhysicalFailure(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	base, _ := NewLocalStore("local", t.TempDir())
	store := &recordingPurgeStore{Store: base, fail: map[string]error{}}
	registry, _ := NewRegistry(db, store.ID(), store)
	failed, _ := registry.Ensure(ctx, Principal(UserPrincipal, "failed"))
	good, _ := registry.Ensure(ctx, Principal(UserPrincipal, "good"))
	for _, key := range []Key{failed.Key, good.Key} {
		if _, err := registry.Tombstone(ctx, key, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	store.fail[failed.ID] = errors.New("disk full")
	worker := &ownerPurgeWorker{deletion: &OwnerDeletion{registry: registry, fencer: &recordingOwnerFencer{}}}
	var snooze *river.JobSnoozeError
	if err := worker.Work(ctx, &river.Job[ownerPurgeArgs]{Args: ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "x", HomeIDs: []string{failed.ID, good.ID}, Actor: "operator"}}); !errors.As(err, &snooze) {
		t.Fatalf("durably recorded physical failure should snooze River: %v", err)
	}
	if record, err := registry.Record(ctx, failed.ID); err != nil || record.State != StatePurgeFailed {
		t.Fatalf("failed record = %#v, %v", record, err)
	}
	if record, err := registry.Record(ctx, good.ID); err != nil || record.State != StatePurged {
		t.Fatalf("remaining record = %#v, %v", record, err)
	}
}

func TestOwnerPurgeRiverSnoozePreservesAttemptAndContinuesAfterChildRecovery(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	base, _ := NewLocalStore("local", t.TempDir())
	store := &recordingPurgeStore{Store: base, fail: map[string]error{}}
	registry, _ := NewRegistry(db, store.ID(), store)
	principal, _ := registry.Ensure(ctx, Principal(UserPrincipal, "river-wait"))
	child, _ := registry.Ensure(ctx, Agent(UserPrincipal, "river-wait", "agent"))
	for _, key := range []Key{principal.Key, child.Key} {
		if _, err := registry.Tombstone(ctx, key, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	store.fail[child.ID] = errors.New("disk full")
	worker := &ownerPurgeWorker{deletion: &OwnerDeletion{registry: registry, fencer: &recordingOwnerFencer{}}}
	testWorker := rivertest.NewWorker(t, riverpgxv5.New(db), &river.Config{}, worker)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	args := ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "river-wait", HomeIDs: []string{principal.ID, child.ID}, Actor: "operator"}
	result, err := testWorker.Work(ctx, t, tx, args, ownerPurgeInsertOpts())
	if err != nil {
		t.Fatal(err)
	}
	if result.EventKind != river.EventKindJobSnoozed || result.Job.Attempt != 0 {
		t.Fatalf("first result = %s attempt %d, want snoozed attempt 0", result.EventKind, result.Job.Attempt)
	}
	delete(store.fail, child.ID)
	if _, err := registry.RetryFailedPurge(ctx, child.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	result, err = testWorker.WorkJob(ctx, t, tx, result.Job)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventKind != river.EventKindJobCompleted {
		t.Fatalf("continuation result = %s, want completed", result.EventKind)
	}
	if record, err := registry.Record(ctx, principal.ID); err != nil || record.State != StatePurged {
		t.Fatalf("parent after River continuation = %#v, %v", record, err)
	}
}

func TestOwnerPurgeRiverSnoozesWhileChildClaimRemainsActive(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store, _ := NewLocalStore("local", t.TempDir())
	registry, _ := NewRegistry(db, store.ID(), store)
	principal, _ := registry.Ensure(ctx, Principal(UserPrincipal, "river-active"))
	child, _ := registry.Ensure(ctx, Agent(UserPrincipal, "river-active", "agent"))
	for _, key := range []Key{principal.Key, child.Key} {
		if _, err := registry.Tombstone(ctx, key, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.q.ClaimStorageHomePurge(ctx, sqlc.ClaimStorageHomePurgeParams{ID: child.ID, PurgeClaimToken: text("active-worker"), ExpectedState: string(StateTombstoned)}); err != nil {
		t.Fatal(err)
	}
	worker := &ownerPurgeWorker{deletion: &OwnerDeletion{registry: registry, fencer: &recordingOwnerFencer{}}}
	testWorker := rivertest.NewWorker(t, riverpgxv5.New(db), &river.Config{}, worker)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	result, err := testWorker.Work(ctx, t, tx, ownerPurgeArgs{OwnerKind: OwnerUser, OwnerID: "river-active", HomeIDs: []string{principal.ID, child.ID}, Actor: "operator"}, ownerPurgeInsertOpts())
	if err != nil {
		t.Fatal(err)
	}
	if result.EventKind != river.EventKindJobSnoozed || result.Job.Attempt != 0 {
		t.Fatalf("active-claim result = %s attempt %d, want snoozed attempt 0", result.EventKind, result.Job.Attempt)
	}
	if record, err := registry.Record(ctx, principal.ID); err != nil || record.State != StateTombstoned {
		t.Fatalf("parent while child claim active = %#v, %v", record, err)
	}
}
