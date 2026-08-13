package skills

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type posixStoreFixture struct {
	store   *POSIXStore
	manager *home.WorkspaceManager
	base    string
	userID  string
	agentID string
}

func newPOSIXStoreFixture(t *testing.T) posixStoreFixture {
	t.Helper()
	db := dbtest.New(t)
	userID, agentID := uuid.NewString(), "home-skill-agent"
	if _, err := db.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'Home Skill Agent','')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store, err := NewPOSIXStore(db, manager)
	if err != nil {
		t.Fatal(err)
	}
	return posixStoreFixture{store: store, manager: manager, base: base, userID: userID, agentID: agentID}
}

func TestManagedSkillLockAcquireUnknownDiscardsSession(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	var released, discarded atomic.Int32
	f.store.acquireManagedLock = func(context.Context) (managedSkillLockSession, error) {
		return managedSkillLockSession{
			lock: func(context.Context) error {
				return errors.Join(home.ErrOutcomeUnknown, errors.New("ack lost after server lock"))
			},
			release: func() { released.Add(1) },
			discard: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("discard did not receive a fresh bounded context")
				}
				discarded.Add(1)
				return nil
			},
		}, nil
	}
	if _, err := f.store.lockManagedMutations(t.Context()); !home.IsOutcomeUnknown(err) {
		t.Fatalf("uncertain acquire = %v", err)
	}
	if released.Load() != 0 || discarded.Load() != 1 {
		t.Fatalf("uncertain acquired session release=%d discard=%d", released.Load(), discarded.Load())
	}
}

func TestManagedSkillLockWaitersDoNotExhaustPool(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	release, err := f.store.lockManagedMutations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	results := make(chan error, 3)
	acquiresBefore := f.store.db.Stat().AcquireCount()
	for range 3 {
		go func() {
			waitingRelease, err := f.store.lockManagedMutations(ctx)
			if err == nil {
				err = waitingRelease()
			}
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for f.store.db.Stat().AcquireCount() < acquiresBefore+3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acquired := f.store.db.Stat().AcquireCount(); acquired < acquiresBefore+3 {
		t.Fatalf("lock waiters did not reach the pool: acquire count %d, want at least %d", acquired, acquiresBefore+3)
	}
	// dbtest uses a four-connection pool. The held lock plus three blocking
	// pg_advisory_lock waiters used to occupy all four and starve this query.
	queryCtx, queryCancel := context.WithTimeout(t.Context(), time.Second)
	var one int
	queryErr := f.store.db.QueryRow(queryCtx, "SELECT 1").Scan(&one)
	queryCancel()
	cancel()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Errorf("canceled lock waiter = %v", err)
		}
	}
	if queryErr != nil || one != 1 {
		t.Fatalf("query behind lock waiters = %d, %v", one, queryErr)
	}
}

func TestManagedSkillLockUnlockTimeoutDiscardsSession(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	var released, discarded atomic.Int32
	f.store.cleanupContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), time.Millisecond)
	}
	f.store.acquireManagedLock = func(context.Context) (managedSkillLockSession, error) {
		return managedSkillLockSession{
			lock: func(context.Context) error { return nil },
			unlock: func(ctx context.Context) (bool, error) {
				<-ctx.Done()
				return false, ctx.Err()
			},
			release: func() { released.Add(1) },
			discard: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("discard did not receive a fresh bounded context")
				}
				discarded.Add(1)
				return nil
			},
		}, nil
	}
	release, err := f.store.lockManagedMutations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); !home.IsOutcomeUnknown(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncertain unlock = %v", err)
	}
	if released.Load() != 0 || discarded.Load() != 1 {
		t.Fatalf("uncertain unlock session release=%d discard=%d", released.Load(), discarded.Load())
	}
}

func fixtureSkill(f posixStoreFixture, id, scope string) Skill {
	skill := Skill{ID: id, Scope: scope, Name: id, Description: "Home current state"}
	switch scope {
	case "system_agent":
		skill.AgentID = f.agentID
	case "user":
		skill.UserID = f.userID
	case "user_agent":
		skill.UserID, skill.AgentID = f.userID, f.agentID
	}
	return skill
}

func TestPOSIXStoreUsesTypedRootsAndLeavesPostgresByteFree(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	for _, test := range []struct {
		scope, id string
		root      string
	}{
		{"system", "system-skill", filepath.Join(f.base, ".agents", "db-skills")},
		{"system_agent", "system-agent-skill", filepath.Join(f.base, "agents", f.agentID, ".agents", "skills")},
		{"user", "user-skill", filepath.Join(f.base, "users", f.userID, ".agents", "skills")},
		{"user_agent", "user-agent-skill", filepath.Join(f.base, "users", f.userID, ".agents", "agent-skills", f.agentID)},
	} {
		t.Run(test.scope, func(t *testing.T) {
			snapshot, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, test.id, test.scope), map[string]string{
				MainFile: "main", "references/data.bin": string([]byte{0, 0xff, 'x'}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !validSkillDigest(snapshot.Skill.ContentDigest) || snapshot.Skill.Description != "Home current state" {
				t.Fatalf("snapshot = %#v", snapshot.Skill)
			}
			target, err := os.Readlink(filepath.Join(test.root, test.id))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.ToSlash(filepath.Join(managedRevisionRoot, test.id, snapshot.Skill.ContentDigest)); target != want {
				t.Fatalf("selector = %q, want %q", target, want)
			}
			revision, err := f.store.LoadExactRevision(t.Context(), snapshot.Skill, snapshot.Skill.ContentDigest)
			loaded := revision.Files["references/data.bin"]
			if err != nil || !bytes.Equal(loaded, []byte{0, 0xff, 'x'}) {
				t.Fatalf("binary load = %q, %v", loaded, err)
			}
		})
	}

	var fileRows int
	if err := f.store.db.QueryRow(t.Context(), "SELECT count(*) FROM skill_file").Scan(&fileRows); err != nil || fileRows != 0 {
		t.Fatalf("PostgreSQL skill_file rows = %d, %v", fileRows, err)
	}
	rows, err := f.store.db.Query(t.Context(), "SELECT description,status,disable_model_invocation,metadata::text FROM skill")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var description, status, metadata string
		var disabled bool
		if err := rows.Scan(&description, &status, &disabled, &metadata); err != nil {
			t.Fatal(err)
		}
		if description != "" || status != SkillStatusActive || disabled || metadata != "{}" {
			t.Fatalf("PostgreSQL retained mutable state: %q %q %v %q", description, status, disabled, metadata)
		}
	}
}

func TestPOSIXStoreDigestCASHasOneConcurrentWinner(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	created, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "cas-skill", "user_agent"), map[string]string{MainFile: "before"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, content := range []string{"first", "second"} {
		go func() {
			<-start
			_, err := f.store.UpdateManagedSkill(context.Background(), ManagedSkillUpdate{
				ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID, AgentID: created.Skill.AgentID,
				ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{MainFile: content},
			})
			errs <- err
		}()
	}
	close(start)
	var succeeded, conflicted int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrSkillDigestConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent update error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if _, err := f.store.UpdateManagedSkill(t.Context(), ManagedSkillUpdate{
		ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID, AgentID: created.Skill.AgentID,
		ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{MainFile: "stale"},
	}); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
}

type faultRootOpener struct {
	base              home.SkillRootOpener
	closeFailure      atomic.Bool
	selectorRemoveErr atomic.Bool
	selectorSyncError atomic.Bool
	rootSyncAlwaysErr atomic.Bool
	rootSyncCalls     atomic.Int32
}

func (o *faultRootOpener) OpenSkillRoot(ctx context.Context, request home.WorkspaceRequest, scope home.RootScope, access home.RootAccess) (home.SkillRootOperations, error) {
	root, err := o.base.OpenSkillRoot(ctx, request, scope, access)
	if err != nil {
		return nil, err
	}
	return &faultSkillRoot{SkillRootOperations: root, opener: o}, nil
}

func (o *faultRootOpener) OpenExistingSkillRoot(ctx context.Context, request home.WorkspaceRequest, scope home.RootScope) (home.SkillRootOperations, error) {
	root, err := o.base.OpenExistingSkillRoot(ctx, request, scope)
	if err != nil {
		return nil, err
	}
	return &faultSkillRoot{SkillRootOperations: root, opener: o}, nil
}

type faultSkillRoot struct {
	home.SkillRootOperations
	opener *faultRootOpener
	once   sync.Once
}

func (r *faultSkillRoot) SyncDirectory(ctx context.Context, directory string) error {
	if directory == "." && r.opener.rootSyncAlwaysErr.Load() {
		return errors.New("injected persistent root fsync failure")
	}
	if directory == "." && r.opener.rootSyncCalls.Add(1) == 2 && r.opener.selectorSyncError.CompareAndSwap(true, false) {
		return errors.New("injected selector fsync failure")
	}
	return r.SkillRootOperations.SyncDirectory(ctx, directory)
}

func (r *faultSkillRoot) Remove(ctx context.Context, name string, options home.RemoveOptions) error {
	if !options.Recursive && r.opener.selectorRemoveErr.CompareAndSwap(true, false) {
		return errors.New("injected selector remove failure")
	}
	return r.SkillRootOperations.Remove(ctx, name, options)
}

func (r *faultSkillRoot) Close() error {
	err := r.SkillRootOperations.Close()
	r.once.Do(func() {
		if r.opener.closeFailure.CompareAndSwap(true, false) {
			err = errors.Join(err, errors.New("injected capability close acknowledgement failure"))
		}
	})
	return err
}

func TestPOSIXStoreReconcilesExactDigestAfterCloseAcknowledgementFailure(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	faults.closeFailure.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "close-reconcile", "user"), map[string]string{MainFile: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.LoadExactRevision(t.Context(), snapshot.Skill, snapshot.Skill.ContentDigest)
	if content := revision.Files[MainFile]; err != nil || string(content) != "committed" {
		t.Fatalf("exact reconciled revision = %q, %v", content, err)
	}
}

func TestPOSIXStoreReconcilesFailedSelectorFsyncBeforeRegisteringIdentity(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	faults.selectorSyncError.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "fsync-reconciled", "user_agent"), map[string]string{MainFile: "committed"})
	if err != nil || !validSkillDigest(created.Skill.ContentDigest) {
		t.Fatalf("reconciled selector durability = %#v, %v", created, err)
	}
	if identity, getErr := store.GetIdentity(t.Context(), "fsync-reconciled"); getErr != nil || identity == nil {
		t.Fatalf("identity absent after exact reconciliation: %#v, %v", identity, getErr)
	}
}

func TestPOSIXStorePersistentPublicationFenceFailureIsOutcomeUnknown(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	faults.rootSyncAlwaysErr.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "fsync-unknown", "user_agent"), map[string]string{MainFile: "uncertain"}); !home.IsOutcomeUnknown(err) {
		t.Fatalf("persistent publication uncertainty = %v", err)
	}
	if identity, getErr := store.GetIdentity(t.Context(), "fsync-unknown"); getErr != nil || identity != nil {
		t.Fatalf("identity registered after uncertain publication: %#v, %v", identity, getErr)
	}
}

func TestPOSIXStoreExactUpdateRetryCompletesEvidenceAfterSelectorOutcomeUnknown(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	faults := &faultRootOpener{base: f.manager}
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManagedSkill(t.Context(), fixtureSkill(f, "update-reconcile", "user"), map[string]string{MainFile: "before"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.loadIdentity(t.Context(), created.Skill)
	if err != nil {
		t.Fatal(err)
	}
	update := ManagedSkillUpdate{
		ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID,
		ExpectedDigest: created.Skill.ContentDigest, Files: map[string]string{MainFile: "after"},
	}
	after := before.Skill
	after.Version++
	after.UpdatedAt = store.now().UTC()
	files, err := mergeRevisionFiles(before.Files, update.Files, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.publish(t.Context(), after, files, update.ExpectedDigest, false); err != nil {
		t.Fatalf("simulate publication before evidence: %v", err)
	}
	retried, err := store.UpdateManagedSkill(t.Context(), update)
	if err != nil || retried.Skill.ContentDigest == created.Skill.ContentDigest {
		t.Fatalf("exact update retry = %#v, %v", retried, err)
	}
}

func TestPOSIXStoreDeleteLeavesCatalogHealthyWhenSelectorCleanupFails(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	deleted, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "delete-cleanup-fails", "user_agent"), map[string]string{MainFile: "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "delete-retained", "user_agent"), map[string]string{MainFile: "retained"})
	if err != nil {
		t.Fatal(err)
	}
	faults := &faultRootOpener{base: f.manager}
	faults.selectorRemoveErr.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	deleteErr := store.DeleteManagedSkill(t.Context(), ManagedSkillDelete{
		ID: deleted.Skill.ID, Scope: deleted.Skill.Scope, UserID: deleted.Skill.UserID, AgentID: deleted.Skill.AgentID,
		ExpectedDigest: deleted.Skill.ContentDigest,
	})
	if !home.IsOutcomeUnknown(deleteErr) {
		t.Fatalf("selector cleanup failure = %v, want outcome unknown", deleteErr)
	}
	if identity, getErr := f.store.GetIdentity(t.Context(), deleted.Skill.ID); getErr != nil || identity != nil {
		t.Fatalf("deleted identity remains after cleanup failure: %#v, %v", identity, getErr)
	}
	rows, listErr := f.store.ListIdentityVisible(t.Context(), ViewContext{UserID: f.userID, AgentID: f.agentID})
	if listErr != nil || len(rows) != 1 || rows[0].ID != retained.Skill.ID {
		t.Fatalf("catalog after interrupted cleanup = %#v, %v", rows, listErr)
	}
	if err := f.store.cleanupDeletedSelection(deleted.Skill, deleted.Skill.ContentDigest); err != nil {
		t.Fatalf("retry selector cleanup: %v", err)
	}
	selector := filepath.Join(f.base, "users", f.userID, ".agents", "agent-skills", f.agentID, deleted.Skill.ID)
	if _, err := os.Lstat(selector); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selector after cleanup retry = %v", err)
	}
}

func TestPOSIXStoreDeleteFailsClosedWhenCurrentSelectorIsMissing(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	created, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "delete-retry", "user"), map[string]string{MainFile: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.removeSelection(t.Context(), created.Skill, created.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
	err = f.store.DeleteManagedSkill(t.Context(), ManagedSkillDelete{
		ID: created.Skill.ID, Scope: created.Skill.Scope, UserID: created.Skill.UserID,
		ExpectedDigest: created.Skill.ContentDigest,
	})
	if !IsCurrentSelectorMissing(err) {
		t.Fatalf("delete without current selector = %v, want selector-missing error", err)
	}
	if identity, getErr := f.store.GetIdentity(t.Context(), created.Skill.ID); getErr != nil || identity == nil {
		t.Fatalf("identity removed without current authority: %#v, %v", identity, getErr)
	}
}

func TestPOSIXStoreDeleteChecksDigestBeforeRemovingIdentity(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	created, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "delete-stale-cas", "system"), map[string]string{MainFile: "current"})
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.DeleteManagedSkill(t.Context(), ManagedSkillDelete{
		ID: created.Skill.ID, Scope: created.Skill.Scope, ExpectedDigest: strings.Repeat("0", 64),
	})
	if !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale delete CAS = %v", err)
	}
	if identity, getErr := f.store.GetIdentity(t.Context(), created.Skill.ID); getErr != nil || identity == nil {
		t.Fatalf("identity removed before CAS check: %#v, %v", identity, getErr)
	}
	if revision, loadErr := f.store.LoadCurrentRevision(t.Context(), created.Skill); loadErr != nil || revision.Skill.ContentDigest != created.Skill.ContentDigest {
		t.Fatalf("current revision after stale delete = %#v, %v", revision.Skill, loadErr)
	}
	root := filepath.Join(f.base, ".agents", "db-skills")
	selector := filepath.Join(root, created.Skill.ID)
	if err := os.Remove(selector); err != nil {
		t.Fatal(err)
	}
	missingDigest := strings.Repeat("2", 64)
	if err := os.Symlink(filepath.ToSlash(filepath.Join(managedRevisionRoot, created.Skill.ID, missingDigest)), selector); err != nil {
		t.Fatal(err)
	}
	err = f.store.DeleteManagedSkill(t.Context(), ManagedSkillDelete{
		ID: created.Skill.ID, Scope: created.Skill.Scope, ExpectedDigest: created.Skill.ContentDigest,
	})
	if err == nil || !errors.Is(err, fs.ErrNotExist) || IsCurrentSelectorMissing(err) {
		t.Fatalf("delete against dangling newer selector = %v, want non-selector fs.ErrNotExist", err)
	}
	if identity, getErr := f.store.GetIdentity(t.Context(), created.Skill.ID); getErr != nil || identity == nil {
		t.Fatalf("identity removed through stale exact revision fallback: %#v, %v", identity, getErr)
	}
	target, readlinkErr := os.Readlink(selector)
	if readlinkErr != nil || !strings.HasSuffix(target, missingDigest) {
		t.Fatalf("dangling current selector was removed: %q, %v", target, readlinkErr)
	}
}

func TestPOSIXStoreCatalogSkipsMissingSelectorButRejectsInvalidRevision(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	missing, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "catalog-missing", "user"), map[string]string{MainFile: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.CreateManagedSkill(t.Context(), fixtureSkill(f, "catalog-retained", "user"), map[string]string{MainFile: "retained"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.removeSelection(t.Context(), missing.Skill, missing.Skill.ContentDigest); err != nil {
		t.Fatal(err)
	}
	identities, err := f.store.ListIdentityVisible(t.Context(), ViewContext{UserID: f.userID})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.loadRows(t.Context(), identities, invocationVisible)
	if err != nil || len(rows) != 1 || rows[0].ID != retained.Skill.ID {
		t.Fatalf("catalog with missing selector = %#v, %v", rows, err)
	}
	root := filepath.Join(f.base, "users", f.userID, ".agents", "skills")
	selector := filepath.Join(root, retained.Skill.ID)
	if err := os.Remove(selector); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.ToSlash(filepath.Join(managedRevisionRoot, retained.Skill.ID, strings.Repeat("1", 64))), selector); err != nil {
		t.Fatal(err)
	}
	identities, err = f.store.ListIdentityVisible(t.Context(), ViewContext{UserID: f.userID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.loadRows(t.Context(), identities, invocationVisible); err == nil || !errors.Is(err, fs.ErrNotExist) || IsCurrentSelectorMissing(err) {
		t.Fatalf("catalog with missing selected revision = %v, want non-selector fs.ErrNotExist", err)
	}
}

func TestPOSIXStoreReflectDeleteLeavesCatalogHealthyWhenSelectorCleanupFails(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	created, err := f.store.CreateReflectOwnedUserAgentSkill(t.Context(), ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: "reflect-delete-cleanup", MainFileContent: "# Reflect cleanup\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	lastUsedAt := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Microsecond)
	if _, err := f.store.db.Exec(t.Context(), `UPDATE skill_usage SET last_used_at=$1 WHERE skill_id=$2`, lastUsedAt, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(t.Context(), `
		INSERT INTO ctx_conversation (id, session_id, channel, kind, agent_id, user_id, last_active)
		VALUES ('00000000-0000-0000-0000-000000000124', 'posix-reflect-delete', 'test', 'chat', $1, $2, $3)
	`, f.agentID, f.userID, lastUsedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	faults := &faultRootOpener{base: f.manager}
	faults.selectorRemoveErr.Store(true)
	store, err := NewPOSIXStore(f.store.db, faults)
	if err != nil {
		t.Fatal(err)
	}
	_, deleteErr := store.DeleteReflectOwnedUserAgentSkill(t.Context(), ReflectSkillDelete{
		ID: created.ID, UserID: f.userID, AgentID: f.agentID,
		ExpectedDigest: created.ContentDigest, ExpectedUsageLastUsedAt: lastUsedAt,
	})
	if !home.IsOutcomeUnknown(deleteErr) {
		t.Fatalf("Reflect selector cleanup failure = %v, want outcome unknown", deleteErr)
	}
	if identity, getErr := f.store.GetIdentity(t.Context(), created.ID); getErr != nil || identity != nil {
		t.Fatalf("Reflect identity remains after cleanup failure: %#v, %v", identity, getErr)
	}
	if rows, listErr := f.store.ListIdentityVisible(t.Context(), ViewContext{UserID: f.userID, AgentID: f.agentID}); listErr != nil || len(rows) != 0 {
		t.Fatalf("catalog after interrupted Reflect cleanup = %#v, %v", rows, listErr)
	}
}

func TestPOSIXStoreReflectDeleteRejectsStaleRevisionBehindDanglingSelector(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	created, err := f.store.CreateReflectOwnedUserAgentSkill(t.Context(), ReflectSkillCreate{
		UserID: f.userID, AgentID: f.agentID, Name: "reflect-delete-dangling", MainFileContent: "# Reflect dangling\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(f.base, "users", f.userID, ".agents", "agent-skills", f.agentID)
	selector := filepath.Join(root, created.ID)
	if err := os.Remove(selector); err != nil {
		t.Fatal(err)
	}
	missingDigest := strings.Repeat("3", 64)
	if err := os.Symlink(filepath.ToSlash(filepath.Join(managedRevisionRoot, created.ID, missingDigest)), selector); err != nil {
		t.Fatal(err)
	}
	_, deleteErr := f.store.DeleteReflectOwnedUserAgentSkill(t.Context(), ReflectSkillDelete{
		ID: created.ID, UserID: f.userID, AgentID: f.agentID,
		ExpectedDigest: created.ContentDigest, ExpectedUsageLastUsedAt: time.Now().UTC(),
	})
	if deleteErr == nil || !errors.Is(deleteErr, fs.ErrNotExist) || IsCurrentSelectorMissing(deleteErr) {
		t.Fatalf("Reflect delete against dangling newer selector = %v, want non-selector fs.ErrNotExist", deleteErr)
	}
	if identity, getErr := f.store.GetIdentity(t.Context(), created.ID); getErr != nil || identity == nil {
		t.Fatalf("Reflect identity removed through stale exact revision fallback: %#v, %v", identity, getErr)
	}
}

type projectionReader struct {
	identities []Skill
	revisions  map[string]ManagedRevision
	loads      int
}

func (*projectionReader) TouchReflectSkillRuntimeUseDigest(context.Context, string, string, string, string) error {
	return nil
}

func (r *projectionReader) GetIdentity(_ context.Context, id string) (*Skill, error) {
	for i := range r.identities {
		if r.identities[i].ID == id {
			return &r.identities[i], nil
		}
	}
	return nil, nil
}

func (r *projectionReader) ListIdentityVisible(context.Context, ViewContext) ([]Skill, error) {
	return append([]Skill(nil), r.identities...), nil
}

func (r *projectionReader) ListIdentityByScope(context.Context, string, string, string) ([]Skill, error) {
	return nil, nil
}

func (r *projectionReader) ListIdentityCandidate(context.Context, string, ViewContext) ([]Skill, error) {
	return nil, nil
}

func (r *projectionReader) LoadCurrentRevision(_ context.Context, identity Skill) (ManagedRevision, error) {
	r.loads++
	revision, ok := r.revisions[identity.ID]
	if !ok {
		return ManagedRevision{}, os.ErrNotExist
	}
	return revision, nil
}

func (r *projectionReader) LoadExactRevision(_ context.Context, identity Skill, digest string) (ManagedRevision, error) {
	revision, err := r.LoadCurrentRevision(context.Background(), identity)
	if err != nil || revision.Skill.ContentDigest != digest {
		return ManagedRevision{}, errors.Join(err, ErrSkillDigestConflict)
	}
	return revision, nil
}

type projectionSession struct {
	tempVisible string
	tempHost    string
}

func (s projectionSession) Policy() pkgsandbox.Policy {
	return pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvTempDir: s.tempVisible}}
}

func (s projectionSession) Files() pkgsandbox.FileAccess { return projectionAccess{session: s} }

type projectionAccess struct{ session projectionSession }

func (a projectionAccess) hostPath(name string) (string, error) {
	s := a.session
	rel, err := filepath.Rel(s.tempVisible, name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return filepath.Join(s.tempHost, rel), nil
}

func (a projectionAccess) ReadFile(string) ([]byte, error) { return nil, os.ErrPermission }
func (a projectionAccess) ReadDir(string) ([]pkgsandbox.DirEntry, error) {
	return nil, os.ErrPermission
}

func (a projectionAccess) Stat(string) (pkgsandbox.FileInfo, error) {
	return pkgsandbox.FileInfo{}, os.ErrPermission
}
func (a projectionAccess) WriteFile(string, []byte, fs.FileMode) error { return os.ErrPermission }
func (a projectionAccess) ProjectFiles(name string, files []pkgsandbox.ProjectedFile) error {
	target, err := a.hostPath(name)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return pkgsandbox.ErrProjectionConflict
		}
		for _, file := range files {
			content, readErr := os.ReadFile(filepath.Join(target, filepath.FromSlash(file.Path)))
			fileInfo, statErr := os.Lstat(filepath.Join(target, filepath.FromSlash(file.Path)))
			if readErr != nil || statErr != nil || !bytes.Equal(content, file.Content) || fileInfo.Mode().Perm() != file.Mode.Perm() {
				return pkgsandbox.ErrProjectionConflict
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".project-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage) //nolint:errcheck
	for _, file := range files {
		path := filepath.Join(stage, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.Content, file.Mode); err != nil {
			return err
		}
	}
	return os.Rename(stage, target)
}

func (a projectionAccess) ProjectTempFiles(name string, files []pkgsandbox.ProjectedFile) (string, error) {
	visible := path.Join(a.session.tempVisible, name)
	if err := a.ProjectFiles(visible, files); err != nil {
		return "", err
	}
	return visible, nil
}

type resilientProjectionSession struct {
	projectionSession
	alive     atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
}

func newResilientProjectionSession(visible, host string) *resilientProjectionSession {
	session := &resilientProjectionSession{
		projectionSession: projectionSession{tempVisible: visible, tempHost: host},
		done:              make(chan struct{}),
	}
	session.alive.Store(true)
	return session
}

func (s *resilientProjectionSession) Alive() bool           { return s.alive.Load() }
func (s *resilientProjectionSession) Done() <-chan struct{} { return s.done }
func (s *resilientProjectionSession) WorkingDir() string    { return s.tempVisible }
func (s *resilientProjectionSession) Close() error {
	s.alive.Store(false)
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *resilientProjectionSession) Exec(context.Context, string, pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	return pkgsandbox.ExecResult{}, nil
}

func (s *resilientProjectionSession) StartProcess(context.Context, pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	return nil, nil
}

type selectedSkillReads struct{ denied map[string]bool }

func (a selectedSkillReads) BeginRead(context.Context) (SkillReadDecision, error) { return a, nil }

func (a selectedSkillReads) AllowRead(_ context.Context, id, _, _, _ string) (bool, error) {
	return !a.denied[id], nil
}

func newProjectionTool(t *testing.T, reader RuntimeReader, session sandboxSession, authorizer SkillReadAuthorizer) *Tool {
	t.Helper()
	tool, err := NewTool(reader, session, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func projectionSkill(id, name, digest string) Skill {
	return Skill{ID: id, Scope: "user_agent", UserID: "user-1", AgentID: "agent-1", Name: name, Status: SkillStatusActive, Version: 1, ContentDigest: digest}
}

func TestManagedLoadAuthorizesIdentityBeforeHomeAndProjectsExactRevision(t *testing.T) {
	digest := strings.Repeat("a", 64)
	identity := projectionSkill("allowed-id", "allowed", "")
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions: map[string]ManagedRevision{identity.ID: {
			Skill: projectionSkill(identity.ID, identity.Name, digest),
			Files: map[string][]byte{MainFile: []byte("current"), "scripts/support.sh": []byte("#!/bin/sh\nprintf support-ok")},
			Modes: map[string]fs.FileMode{MainFile: 0o644, "scripts/support.sh": 0o755},
		}},
	}
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	denied := newProjectionTool(t, reader, session, selectedSkillReads{denied: map[string]bool{identity.ID: true}})
	if _, err := denied.load(t.Context(), map[string]any{"name": identity.Name}); !errors.Is(err, errSkillNotFound) {
		t.Fatalf("denied load = %v", err)
	}
	if reader.loads != 0 {
		t.Fatalf("Home revision opened before authorization: loads=%d", reader.loads)
	}

	tool := newProjectionTool(t, reader, session, selectedSkillReads{})
	out, err := tool.load(t.Context(), map[string]any{"name": identity.Name})
	if err != nil {
		t.Fatal(err)
	}
	wantVisible := filepath.Join(session.tempVisible, "stella-skills", identity.Scope, identity.ID, digest)
	if !strings.Contains(out, "<skill_dir>"+wantVisible+"</skill_dir>") || !strings.Contains(out, "current") {
		t.Fatalf("load output = %q", out)
	}
	host := filepath.Join(session.tempHost, "stella-skills", identity.Scope, identity.ID, digest)
	if content, err := os.ReadFile(filepath.Join(host, "scripts", "support.sh")); err != nil || string(content) != "#!/bin/sh\nprintf support-ok" {
		t.Fatalf("projected support file = %q, %v", content, err)
	}
	if info, err := os.Stat(filepath.Join(host, "scripts", "support.sh")); err != nil || info.Mode().Perm()&0o222 != 0 || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("projected support mode = %v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(host))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			t.Fatalf("projection exposed unpublished stage %q", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(session.tempHost, managedRevisionRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authority catalog appeared in Session projection: %v", err)
	}
}

func TestManagedLoadUsesIsolatedSessionTempView(t *testing.T) {
	digest := strings.Repeat("9", 64)
	identity := projectionSkill("docker-view", "docker-view", "")
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions: map[string]ManagedRevision{identity.ID: {
			Skill: projectionSkill(identity.ID, identity.Name, digest),
			Files: map[string][]byte{MainFile: []byte("isolated current"), "scripts/run.sh": []byte("#!/bin/sh\nprintf isolated")},
			Modes: map[string]fs.FileMode{MainFile: 0o644, "scripts/run.sh": 0o755},
		}},
	}
	session := projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}
	tool := newProjectionTool(t, reader, session, selectedSkillReads{})
	out, err := tool.load(t.Context(), map[string]any{"name": identity.Name})
	if err != nil {
		t.Fatal(err)
	}
	wantVisible := filepath.Join("/tmp", "stella-skills", identity.Scope, identity.ID, digest)
	if !strings.Contains(out, "<skill_dir>"+wantVisible+"</skill_dir>") || strings.Contains(out, session.tempHost) {
		t.Fatalf("isolated load output = %q", out)
	}
	hostFile := filepath.Join(session.tempHost, "stella-skills", identity.Scope, identity.ID, digest, "scripts", "run.sh")
	if content, err := os.ReadFile(hostFile); err != nil || string(content) != "#!/bin/sh\nprintf isolated" {
		t.Fatalf("isolated host projection = %q, %v", content, err)
	}
}

func TestManagedLoadRefreshesSessionTempBeforeProjection(t *testing.T) {
	digest := strings.Repeat("8", 64)
	identity := projectionSkill("recreated-view", "recreated-view", "")
	reader := &projectionReader{
		identities: []Skill{identity},
		revisions: map[string]ManagedRevision{identity.ID: {
			Skill: projectionSkill(identity.ID, identity.Name, digest),
			Files: map[string][]byte{MainFile: []byte("recreated current")},
			Modes: map[string]fs.FileMode{MainFile: 0o644},
		}},
	}
	first := newResilientProjectionSession("/old/tmp", t.TempDir())
	second := newResilientProjectionSession("/new/tmp", t.TempDir())
	session := pkgsandbox.NewResilientSession(first, func(context.Context) (pkgsandbox.Session, error) {
		return second, nil
	})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	tool := newProjectionTool(t, reader, session, selectedSkillReads{})
	out, err := tool.load(t.Context(), map[string]any{"name": identity.Name})
	if err != nil {
		t.Fatal(err)
	}
	wantVisible := filepath.Join("/new/tmp", "stella-skills", identity.Scope, identity.ID, digest)
	if !strings.Contains(out, "<skill_dir>"+wantVisible+"</skill_dir>") || strings.Contains(out, "/old/tmp") {
		t.Fatalf("recreated load output = %q", out)
	}
	if content, err := os.ReadFile(filepath.Join(second.tempHost, "stella-skills", identity.Scope, identity.ID, digest, MainFile)); err != nil || string(content) != "recreated current" {
		t.Fatalf("recreated projection = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(first.tempHost, "stella-skills")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead Session received projection: %v", err)
	}
}

func TestManagedProjectionConcurrentExactDigestPublishesOneCompletePath(t *testing.T) {
	digest := strings.Repeat("f", 64)
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	tool := newProjectionTool(t, &projectionReader{}, session, selectedSkillReads{})
	revision := ManagedRevision{
		Skill: projectionSkill("concurrent-id", "concurrent", digest),
		Files: map[string][]byte{
			MainFile:             []byte("# Concurrent\n"),
			"scripts/support.sh": []byte("#!/bin/sh\nprintf complete"),
		},
		Modes: map[string]fs.FileMode{MainFile: 0o644, "scripts/support.sh": 0o755},
	}

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	paths := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			<-start
			var projected string
			projection, err := managedSkillProjection(revision)
			if err == nil {
				projected, err = tool.projectSkill(projection)
			}
			paths <- projected
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(paths)
	close(errs)
	want := filepath.Join(session.tempVisible, "stella-skills", revision.Skill.Scope, revision.Skill.ID, digest)
	for projected := range paths {
		if projected != want {
			t.Errorf("projection path = %q, want %q", projected, want)
		}
	}
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent projection: %v", err)
		}
	}
	host := filepath.Join(session.tempHost, "stella-skills", revision.Skill.Scope, revision.Skill.ID, digest)
	if content, err := os.ReadFile(filepath.Join(host, "scripts", "support.sh")); err != nil || string(content) != "#!/bin/sh\nprintf complete" {
		t.Fatalf("published projection = %q, %v", content, err)
	}
	entries, err := os.ReadDir(filepath.Dir(host))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != digest {
		t.Fatalf("projection catalog = %#v", entries)
	}
}

func TestManagedLoadPinsEverySessionDigestAndExcludesDisabledOrPolicyDeniedSkills(t *testing.T) {
	oldDigest, newDigest := strings.Repeat("b", 64), strings.Repeat("c", 64)
	current := projectionSkill("current-id", "current", "")
	disabled := projectionSkill("disabled-id", "disabled", "")
	policyDenied := projectionSkill("policy-id", "policy", "")
	policyDenied.Scope = "system_agent"
	policyDenied.UserID = ""
	reader := &projectionReader{
		identities: []Skill{current, disabled, policyDenied},
		revisions: map[string]ManagedRevision{
			current.ID: {Skill: projectionSkill(current.ID, current.Name, oldDigest), Files: map[string][]byte{MainFile: []byte("old-current")}, Modes: map[string]fs.FileMode{MainFile: 0o644}},
			disabled.ID: {Skill: func() Skill {
				s := projectionSkill(disabled.ID, disabled.Name, strings.Repeat("d", 64))
				s.DisableModelInvocation = true
				return s
			}(), Files: map[string][]byte{MainFile: []byte("must-not-project")}, Modes: map[string]fs.FileMode{MainFile: 0o644}},
			policyDenied.ID: {Skill: func() Skill { s := policyDenied; s.ContentDigest = strings.Repeat("e", 64); return s }(), Files: map[string][]byte{MainFile: []byte("must-not-project-policy")}, Modes: map[string]fs.FileMode{MainFile: 0o644}},
		},
	}
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	tool := newProjectionTool(t, reader, session, selectedSkillReads{}).
		WithAgentSkillPolicy([]string{"system_agent:policy"})
	if _, err := tool.load(t.Context(), map[string]any{"name": current.Name}); err != nil {
		t.Fatal(err)
	}
	oldHost := filepath.Join(session.tempHost, "stella-skills", current.Scope, current.ID, oldDigest)
	if _, err := os.Stat(oldHost); err != nil {
		t.Fatal(err)
	}
	reader.revisions[current.ID] = ManagedRevision{Skill: projectionSkill(current.ID, current.Name, newDigest), Files: map[string][]byte{MainFile: []byte("new-current")}, Modes: map[string]fs.FileMode{MainFile: 0o644}}
	if _, err := tool.load(t.Context(), map[string]any{"name": current.Name}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(oldHost, MainFile)); err != nil || string(content) != "old-current" {
		t.Fatalf("pinned historical projection = %q, %v", content, err)
	}
	newHost := filepath.Join(session.tempHost, "stella-skills", current.Scope, current.ID, newDigest)
	if content, err := os.ReadFile(filepath.Join(newHost, MainFile)); err != nil || string(content) != "new-current" {
		t.Fatalf("new exact projection = %q, %v", content, err)
	}
	if _, err := tool.load(t.Context(), map[string]any{"name": disabled.Name}); !errors.Is(err, errSkillNotFound) {
		t.Fatalf("disable_model_invocation load = %v", err)
	}
	if _, err := tool.load(t.Context(), map[string]any{"name": policyDenied.Name}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("policy-denied load = %v", err)
	}
	if err := filepath.WalkDir(session.tempHost, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			content, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), "must-not-project") {
				t.Errorf("excluded Skill content projected at %q", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedProjectionRejectsPoisonedExactDigestReuse(t *testing.T) {
	digest := strings.Repeat("7", 64)
	session := projectionSession{tempVisible: "/session/tmp", tempHost: t.TempDir()}
	tool := newProjectionTool(t, &projectionReader{}, session, selectedSkillReads{})
	revision := ManagedRevision{
		Skill: projectionSkill("poisoned-id", "poisoned", digest),
		Files: map[string][]byte{
			MainFile:             []byte("# Exact\n"),
			"scripts/support.sh": []byte("#!/bin/sh\nprintf exact"),
		},
		Modes: map[string]fs.FileMode{MainFile: 0o644, "scripts/support.sh": 0o755},
	}
	projection, err := managedSkillProjection(revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.projectSkill(projection); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(session.tempHost, "stella-skills", revision.Skill.Scope, revision.Skill.ID, digest)
	poisoned := filepath.Join(host, "scripts", "support.sh")
	if err := os.Chmod(poisoned, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poisoned, []byte("poisoned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.projectSkill(projection); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("poisoned projection reuse = %v, want ErrInvalidSkillRevision", err)
	}
	content, err := os.ReadFile(filepath.Join(host, "scripts", "support.sh"))
	if err != nil || string(content) != "poisoned" {
		t.Fatalf("poisoned path was silently replaced: %q, %v", content, err)
	}
}
