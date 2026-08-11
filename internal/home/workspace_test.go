package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type (
	testOwnerFencer     struct{}
	testOwnerFenceLease struct{}
)

func (testOwnerFenceLease) Commit()  {}
func (testOwnerFenceLease) Release() {}
func (testOwnerFencer) AcquireHomeOwnerFence(context.Context, OwnerKind, string) (OwnerFenceLease, error) {
	return testOwnerFenceLease{}, nil
}

func TestWorkspaceViewKeepsTypedPrincipalsDisjointAndSharedRootsReadOnly(t *testing.T) {
	ctx := context.Background()
	r, store := newRegistry(t)
	for _, key := range []Key{SystemSkills(), SystemAgentSkills("a")} {
		if _, err := r.Ensure(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(store.base, "users", "abc", "data", "kept")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	user, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "abc", AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "abc", GroupID: "abc", AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if user.Principal.HomeID == group.Principal.HomeID || user.PrincipalRoot == group.PrincipalRoot {
		t.Fatal("equal raw user/group IDs shared a Home")
	}
	after, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("workspace materialization moved existing data")
	}
	if !user.SystemSkillRoot.ReadOnly || !user.SystemAgentSkillRoot.ReadOnly {
		t.Fatal("shared Skill roots must be read-only")
	}
	if _, err := r.WorkspaceView(ctx, WorkspaceRequest{AgentID: "a"}); err != nil {
		t.Fatalf("user-less shared roots: %v", err)
	}
}

func TestWorkspaceViewUsesAdvisoryLockTransactionWithoutSecondPoolCheckout(t *testing.T) {
	base := dbtest.New(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 1
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "u", AgentID: "a"}); err != nil {
		t.Fatalf("WorkspaceView with one pool connection: %v", err)
	}
}

type blockingWorkspaceStore struct {
	Store
	local   *LocalStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingWorkspaceStore) PrepareWorkspace(principal, agent Record) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.local.PrepareWorkspace(principal, agent)
}

func (s *blockingWorkspaceStore) WorkspacePaths(principal, agent Record) (string, string, string, error) {
	return s.local.WorkspacePaths(principal, agent)
}

func TestWorkspaceViewProjectionLinearizesBeforeOwnerDeletion(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name   string
		seed   func(*testing.T, *sqlc.Queries, *pgxpool.Pool, string)
		delete func(*OwnerDeletion, context.Context, string) error
		req    func(string) WorkspaceRequest
	}{
		{
			name: "user",
			seed: func(t *testing.T, _ *sqlc.Queries, db *pgxpool.Pool, id string) {
				t.Helper()
				if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", id, id+"@example.test"); err != nil {
					t.Fatal(err)
				}
			},
			delete: func(d *OwnerDeletion, ctx context.Context, id string) error { return d.DeleteUser(ctx, id, "operator") },
			req:    func(id string) WorkspaceRequest { return WorkspaceRequest{UserID: id, AgentID: "workspace-agent"} },
		},
		{
			name: "group",
			seed: func(t *testing.T, q *sqlc.Queries, _ *pgxpool.Pool, id string) {
				t.Helper()
				if _, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: id, Platform: "test", PlatformGroupID: id, GroupName: "group"}); err != nil {
					t.Fatal(err)
				}
			},
			delete: func(d *OwnerDeletion, ctx context.Context, id string) error {
				return d.DeleteGroup(ctx, id, "operator")
			},
			req: func(id string) WorkspaceRequest {
				return WorkspaceRequest{UserID: id, GroupID: id, AgentID: "workspace-agent"}
			},
		},
		{
			name: "agent",
			seed: func(t *testing.T, q *sqlc.Queries, _ *pgxpool.Pool, id string) {
				t.Helper()
				if err := q.SeedAgent(ctx, sqlc.SeedAgentParams{ID: id, Name: "agent", Model: "test", SystemPrompt: "", Workspace: "", Sandbox: []byte(`{}`), Scope: "system", Enabled: true}); err != nil {
					t.Fatal(err)
				}
			},
			delete: func(d *OwnerDeletion, ctx context.Context, id string) error {
				return d.DeleteAgent(ctx, id, "operator")
			},
			req: func(id string) WorkspaceRequest { return WorkspaceRequest{UserID: "workspace-user", AgentID: id} },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := dbtest.New(t)
			cfg := base.Config().Copy()
			cfg.MaxConns = 1
			db, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			local, err := NewLocalStore("local", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store := &blockingWorkspaceStore{Store: local, local: local, entered: make(chan struct{}), release: make(chan struct{})}
			registry, err := NewRegistry(db, local.ID(), store)
			if err != nil {
				t.Fatal(err)
			}
			q := sqlc.New(db)
			ownerID := uuid.NewString()
			tt.seed(t, q, db, ownerID)
			deletion, err := NewOwnerDeletion(db, registry, testOwnerFencer{})
			if err != nil {
				t.Fatal(err)
			}
			viewDone := make(chan error, 1)
			go func() { _, err := registry.WorkspaceView(ctx, tt.req(ownerID)); viewDone <- err }()
			<-store.entered
			// If projection retained a transaction, this MaxConns=1 query would block.
			queryCtx, cancel := context.WithTimeout(ctx, time.Second)
			err = db.Ping(queryCtx)
			cancel()
			if err != nil {
				t.Fatalf("database unavailable while projection blocks: %v", err)
			}
			deleteDone := make(chan error, 1)
			go func() { deleteDone <- tt.delete(deletion, ctx, ownerID) }()
			select {
			case err := <-deleteDone:
				t.Fatalf("delete completed before projection: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(store.release)
			if err := <-viewDone; err != nil {
				t.Fatalf("WorkspaceView: %v", err)
			}
			if err := <-deleteDone; err != nil {
				t.Fatalf("delete after view: %v", err)
			}
			if _, err := registry.WorkspaceView(ctx, tt.req(ownerID)); err == nil {
				t.Fatal("subsequent WorkspaceView succeeded after deletion")
			}
		})
	}
}

func TestWorkspaceViewOwnerLockHonorsCanceledContext(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &blockingWorkspaceStore{Store: local, local: local, entered: make(chan struct{}), release: make(chan struct{})}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	req := WorkspaceRequest{UserID: "same-owner", AgentID: "agent"}
	firstDone := make(chan error, 1)
	go func() { _, err := r.WorkspaceView(ctx, req); firstDone <- err }()
	<-store.entered
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	secondDone := make(chan error, 1)
	go func() { _, err := r.WorkspaceView(canceled, req); secondDone <- err }()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled WorkspaceView error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled WorkspaceView waited for the blocked owner lock")
	}
	close(store.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first WorkspaceView: %v", err)
	}
}

func TestWorkspaceViewRejectsNestedSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	r, store := newRegistry(t)
	for _, key := range []Key{SystemSkills(), SystemAgentSkills("a")} {
		if _, err := r.Ensure(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	data := filepath.Join(store.base, "users", "u", "data")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, data); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WorkspaceView(ctx, WorkspaceRequest{UserID: "u", AgentID: "a"}); err == nil {
		t.Fatal("WorkspaceView succeeded through escaping data symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, ".cache")); !os.IsNotExist(err) {
		t.Fatalf("outside store was modified: %v", err)
	}
}

func TestWorkspaceAttachmentRevalidationRejectsForgedAndTombstonedRecords(t *testing.T) {
	ctx := context.Background()
	r, _ := newRegistry(t)
	record, err := r.Ensure(ctx, Principal(UserPrincipal, "u"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forged := range []Record{
		{ID: record.ID, Key: record.Key, StoreID: "other", Locator: record.Locator, State: StateReady},
		{ID: record.ID, Key: Principal(GroupPrincipal, "u"), StoreID: record.StoreID, Locator: record.Locator, State: StateReady},
		{ID: record.ID, Key: record.Key, StoreID: record.StoreID, Locator: "users/other", State: StateReady},
	} {
		if _, err := r.resolveRecord(ctx, forged, false); err == nil {
			t.Fatalf("forged record %+v resolved", forged)
		}
	}
	if _, err := r.Tombstone(ctx, record.Key, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolveRecord(ctx, record, false); err == nil {
		t.Fatal("tombstoned record resolved")
	}
}
