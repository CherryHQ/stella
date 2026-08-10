package home

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newRegistry(t *testing.T) (*Registry, *LocalStore) {
	t.Helper()
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(dbtest.New(t), store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	return r, store
}

func TestKeyValidationKeepsTypedOwnersDisjoint(t *testing.T) {
	for _, key := range []Key{
		Principal(UserPrincipal, "abc"), Principal(GroupPrincipal, "abc"),
		Agent(UserPrincipal, "abc", "agent"), SystemSkills(), SystemAgentSkills("agent"),
	} {
		if err := key.Validate(); err != nil {
			t.Fatalf("%+v: %v", key, err)
		}
	}
	for _, key := range []Key{
		{Kind: PrincipalHome},
		{Kind: AgentHome, PrincipalKind: UserPrincipal, PrincipalID: "abc"},
		{Kind: PrincipalHome, PrincipalKind: UserPrincipal, PrincipalID: "../escape"},
		{Kind: SystemAgentSkillRoot},
	} {
		if err := key.Validate(); err == nil {
			t.Fatalf("%+v unexpectedly valid", key)
		}
	}
}

func TestRegistryConstructorRejectsInvalidDependencies(t *testing.T) {
	if _, err := NewRegistry(nil, "local"); err == nil {
		t.Fatal("nil database accepted")
	}
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(dbtest.New(t), "", store); err == nil {
		t.Fatal("empty default store accepted")
	}
	if _, err := NewLocalStore("bad/store", t.TempDir()); err == nil {
		t.Fatal("path-like store ID accepted")
	}
}

func TestEnsureConcurrentPreservesLegacyInodeAndUsesLocator(t *testing.T) {
	r, store := newRegistry(t)
	locator, err := store.Allocate(Principal(UserPrincipal, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.base, filepath.FromSlash(locator))
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "keep")
	if err := os.WriteFile(file, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	key := Principal(UserPrincipal, "abc")
	results := make(chan Record, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			home, err := r.Ensure(context.Background(), key)
			if err != nil {
				errs <- err
				return
			}
			results <- home
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var homes []Record
	for home := range results {
		homes = append(homes, home)
	}
	if len(homes) != 2 || homes[0].ID != homes[1].ID || homes[0].Locator != locator || homes[0].State != StateReady {
		t.Fatalf("concurrent ensure = %+v", homes)
	}
	attachment, err := r.Resolve(context.Background(), key, false)
	if err != nil || attachment.Locator != locator || attachment.Locator == store.base {
		t.Fatalf("attachment = %+v, %v", attachment, err)
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("Ensure copied or renamed existing data")
	}
}

func TestLocalStoreRejectsEscapingSymlink(t *testing.T) {
	r, store := newRegistry(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(store.base, "users")); err != nil {
		t.Fatal(err)
	}
	_, err := r.Ensure(context.Background(), Principal(UserPrincipal, "abc"))
	if err == nil {
		t.Fatal("Ensure followed escaping users symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "abc")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Ensure created bytes outside store: %v", statErr)
	}
}

type blockingEnsureStore struct {
	Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockingEnsureStore) Ensure(ctx context.Context, home Record) error {
	close(s.entered)
	<-s.release
	return s.Store.Ensure(ctx, home)
}

func TestEnsureFailsClosedWhenTombstoneWinsReadyTransition(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingEnsureStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	r, err := NewRegistry(dbtest.New(t), store.ID(), blocking)
	if err != nil {
		t.Fatal(err)
	}
	key := Principal(UserPrincipal, "abc")
	done := make(chan error, 1)
	go func() {
		_, err := r.Ensure(context.Background(), key)
		done <- err
	}()
	<-blocking.entered
	if _, err := r.Tombstone(context.Background(), key, "test-admin"); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-done; err == nil {
		t.Fatal("Ensure succeeded after tombstone won the ready transition")
	}
}

func TestRegistryConstraintsLifecycleAndPurgeRetries(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	flaky := &failingPurgeStore{Store: store, fail: true}
	r, err := NewRegistry(dbtest.New(t), store.ID(), flaky)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, err := r.Ensure(ctx, Principal(UserPrincipal, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := r.Ensure(ctx, Principal(GroupPrincipal, "abc"))
	if err != nil || user.ID == group.ID || user.Locator == group.Locator {
		t.Fatalf("raw-ID isolation = %+v / %+v, %v", user, group, err)
	}
	system1, err := r.Ensure(ctx, SystemSkills())
	if err != nil {
		t.Fatal(err)
	}
	system2, err := r.Ensure(ctx, SystemSkills())
	if err != nil || system1.ID != system2.ID {
		t.Fatalf("system singleton = %+v / %+v, %v", system1, system2, err)
	}
	agentRoot1, err := r.Ensure(ctx, SystemAgentSkills("agent"))
	if err != nil {
		t.Fatal(err)
	}
	agentRoot2, err := r.Ensure(ctx, SystemAgentSkills("agent"))
	if err != nil || agentRoot1.ID != agentRoot2.ID {
		t.Fatalf("system Agent singleton = %+v / %+v, %v", agentRoot1, agentRoot2, err)
	}
	shared, err := r.Resolve(ctx, SystemSkills(), false)
	if err != nil || !shared.ReadOnly {
		t.Fatalf("system attachment = %+v, %v", shared, err)
	}
	if _, err := r.Tombstone(ctx, Principal(UserPrincipal, "abc"), ""); err == nil {
		t.Fatal("blank tombstone actor accepted")
	}
	if _, err := r.Tombstone(ctx, Principal(UserPrincipal, "abc"), "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(ctx, Principal(UserPrincipal, "abc")); err == nil {
		t.Fatal("tombstoned Home ensured")
	}
	if _, err := r.Purge(ctx, user.ID, "admin"); err == nil {
		t.Fatal("injected physical purge failure succeeded")
	}
	row, err := r.q.GetStorageHome(ctx, user.ID)
	if err != nil || !row.PurgeRequestedAt.Valid || !row.PurgeFailedAt.Valid {
		t.Fatalf("purge audit = %+v, %v", row, err)
	}
	if _, err := r.Ensure(ctx, Principal(UserPrincipal, "abc")); err == nil {
		t.Fatal("purge_failed Home ensured")
	}
	if _, err := r.Purge(ctx, user.ID, ""); err == nil {
		t.Fatal("blank purge actor accepted")
	}
	if _, err := r.RetryFailedPurge(ctx, group.ID, "admin"); err == nil {
		t.Fatal("tombstoned Home accepted as an admin retry")
	}
	flaky.fail = false
	purged, err := r.RetryFailedPurge(ctx, user.ID, "admin")
	if err != nil || purged.State != StatePurged {
		t.Fatalf("purge retry = %+v, %v", purged, err)
	}
	again, err := r.Purge(ctx, user.ID, "admin")
	if err != nil || again.State != StatePurged || again.ID != user.ID {
		t.Fatalf("duplicate purge = %+v, %v", again, err)
	}
	if _, err := r.Resolve(ctx, Principal(UserPrincipal, "abc"), false); err == nil {
		t.Fatal("purged Home resolved")
	}
}

type failingPurgeStore struct {
	Store
	fail bool
}

func (s *failingPurgeStore) Purge(ctx context.Context, home Record) error {
	if s.fail {
		return errors.New("injected physical delete failure")
	}
	return s.Store.Purge(ctx, home)
}

func TestRetryFailedPurgeClaimsEligibilityOnce(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	flaky := &failingPurgeStore{Store: store, fail: true}
	r, err := NewRegistry(dbtest.New(t), store.ID(), flaky)
	if err != nil {
		t.Fatal(err)
	}
	record, err := r.Ensure(context.Background(), Principal(UserPrincipal, "retry-race"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tombstone(context.Background(), record.Key, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Purge(context.Background(), record.ID, "admin"); err == nil {
		t.Fatal("initial failure unexpectedly succeeded")
	}
	flaky.fail = false
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := r.RetryFailedPurge(context.Background(), record.ID, "admin")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var success, rejected int
	for err := range results {
		if err == nil {
			success++
		} else {
			rejected++
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("retry results success=%d rejected=%d, want one each", success, rejected)
	}
}

func TestLocalStoreFailedPurgePreservesAuditAndRetryRemovesBytes(t *testing.T) {
	ctx := context.Background()
	base, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	flaky := &failingPurgeStore{Store: base, fail: true}
	r, err := NewRegistry(dbtest.New(t), base.ID(), flaky)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := r.Ensure(ctx, Principal(UserPrincipal, "purge-retry"))
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(base.base, filepath.FromSlash(ready.Locator), "payload")
	if err := os.WriteFile(payload, []byte("durable bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	tombstoned, err := r.Tombstone(ctx, ready.Key, "delete-actor")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Purge(ctx, ready.ID, "purge-actor")
	var physical *PhysicalPurgeError
	if !errors.As(err, &physical) {
		t.Fatalf("Purge error = %v, want PhysicalPurgeError", err)
	}
	failed, err := r.Record(ctx, ready.ID)
	if err != nil || failed.State != StatePurgeFailed {
		t.Fatalf("failed record = %#v, %v", failed, err)
	}
	if failed.ID != ready.ID || failed.Key != ready.Key || failed.StoreID != ready.StoreID || failed.Locator != ready.Locator {
		t.Fatalf("failed identity = %#v, want %#v", failed, ready)
	}
	row, err := r.q.GetStorageHome(ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.TombstonedAt.Valid || !row.PurgeRequestedAt.Valid || !row.PurgeFailedAt.Valid || !row.TombstonedBy.Valid || row.TombstonedBy.String != "delete-actor" || !row.LastPurgeError.Valid {
		t.Fatalf("failed purge audit = %#v", row)
	}
	if tombstoned.ID != ready.ID {
		t.Fatalf("tombstoned record = %#v, want %s", tombstoned, ready.ID)
	}
	flaky.fail = false
	purged, err := r.RetryFailedPurge(ctx, ready.ID, "retry-actor")
	if err != nil || purged.State != StatePurged {
		t.Fatalf("RetryFailedPurge = %#v, %v", purged, err)
	}
	if _, err := os.Stat(payload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purged bytes stat = %v, want absent", err)
	}
	for _, tt := range []struct {
		name string
		id   string
	}{
		{name: "ready", id: mustEnsureHome(t, r, Principal(UserPrincipal, "retry-ready")).ID},
		{name: "tombstoned", id: mustTombstoneHome(t, r, Principal(UserPrincipal, "retry-tombstoned")).ID},
		{name: "purged", id: ready.ID},
		{name: "missing", id: "00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := r.RetryFailedPurge(ctx, tt.id, "retry-actor"); err == nil {
				t.Fatal("RetryFailedPurge accepted an ineligible Home")
			}
		})
	}
}

func mustEnsureHome(t *testing.T, r *Registry, key Key) Record {
	t.Helper()
	record, err := r.Ensure(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func mustTombstoneHome(t *testing.T, r *Registry, key Key) Record {
	t.Helper()
	record := mustEnsureHome(t, r, key)
	if _, err := r.Tombstone(context.Background(), key, "delete-actor"); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestConcurrentPurgeTreatsWinnerAsSuccess(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	home, err := r.Ensure(ctx, Principal(UserPrincipal, "user"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tombstone(ctx, home.Key, "admin"); err != nil {
		t.Fatal(err)
	}
	results := make(chan Record, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			record, err := r.Purge(ctx, home.ID, "admin")
			if err != nil {
				errs <- err
				return
			}
			results <- record
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	var inProgress int
	for err := range errs {
		if !errors.Is(err, ErrPurgeInProgress) {
			t.Fatal(err)
		}
		inProgress++
	}
	var success int
	for record := range results {
		if record.State != StatePurged {
			t.Fatalf("concurrent purge state = %s", record.State)
		}
		success++
	}
	if success < 1 || success+inProgress != 2 {
		t.Fatalf("concurrent purge success=%d in-progress=%d", success, inProgress)
	}
}

type blockingPurgeStore struct {
	Store
	mu      sync.Mutex
	calls   int
	fail    bool
	block   bool
	entered chan struct{}
	release chan struct{}
}

func (s *blockingPurgeStore) Purge(ctx context.Context, record Record) error {
	s.mu.Lock()
	s.calls++
	fail, block := s.fail, s.block
	s.mu.Unlock()
	if fail {
		return errors.New("injected physical failure")
	}
	if block {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.Purge(ctx, record)
}

func TestPurgeClaimExcludesOrdinaryAndRetryOverlap(t *testing.T) {
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &blockingPurgeStore{Store: local, fail: true, entered: make(chan struct{}, 1), release: make(chan struct{})}
	r, err := NewRegistry(dbtest.New(t), local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	record := mustTombstoneHome(t, r, Principal(UserPrincipal, "exclusive-purge"))
	if _, err := r.Purge(ctx, record.ID, "worker"); err == nil {
		t.Fatal("initial failure unexpectedly succeeded")
	}
	parked, err := r.Purge(ctx, record.ID, "worker-retry")
	if err != nil || parked.State != StatePurgeFailed {
		t.Fatalf("ordinary retry did not leave physical failure parked: %#v, %v", parked, err)
	}
	store.mu.Lock()
	if store.calls != 1 {
		t.Fatalf("ordinary retry repeated parked physical purge: calls=%d", store.calls)
	}
	store.mu.Unlock()
	store.mu.Lock()
	store.fail, store.block = false, true
	store.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := r.RetryFailedPurge(ctx, record.ID, "operator")
		done <- err
	}()
	<-store.entered
	if _, err := r.Purge(ctx, record.ID, "worker-retry"); !errors.Is(err, ErrPurgeInProgress) {
		t.Fatalf("overlapping ordinary purge = %v, want ErrPurgeInProgress", err)
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	calls := store.calls
	store.mu.Unlock()
	if calls != 2 {
		t.Fatalf("physical purge calls = %d, want initial failure plus one claimed retry", calls)
	}
}

func TestExpiredPurgeClaimIsRecovered(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	record := mustTombstoneHome(t, r, Principal(UserPrincipal, "expired-claim"))
	if _, err := r.q.ClaimStorageHomePurge(ctx, sqlc.ClaimStorageHomePurgeParams{ID: record.ID, PurgeClaimToken: text("crashed-worker")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.MarkStorageHomePurged(ctx, sqlc.MarkStorageHomePurgedParams{ID: record.ID, PurgedBy: text("intruder"), PurgeClaimToken: text("wrong-token")}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong claim token completion = %v, want no rows", err)
	}
	if _, err := r.Purge(ctx, record.ID, "replacement"); !errors.Is(err, ErrPurgeInProgress) {
		t.Fatalf("active claim purge = %v, want ErrPurgeInProgress", err)
	}
	if _, err := r.db.Exec(ctx, "UPDATE storage_home SET purge_claim_until = now() - interval '1 second' WHERE id = $1", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.MarkStorageHomePurged(ctx, sqlc.MarkStorageHomePurgedParams{ID: record.ID, PurgedBy: text("expired-worker"), PurgeClaimToken: text("crashed-worker")}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired claim completion = %v, want no rows", err)
	}
	purged, err := r.Purge(ctx, record.ID, "replacement")
	if err != nil || purged.State != StatePurged {
		t.Fatalf("expired claim recovery = %#v, %v", purged, err)
	}
}

func TestExistingHomeKeepsStoreAndCutoverValidatesLocator(t *testing.T) {
	db := dbtest.New(t)
	first, err := NewLocalStore("first", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalStore("second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r1, err := NewRegistry(db, first.ID(), first, second)
	if err != nil {
		t.Fatal(err)
	}
	home, err := r1.Ensure(context.Background(), Principal(UserPrincipal, "user"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := NewRegistry(db, second.ID(), first, second)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r2.Ensure(context.Background(), Principal(UserPrincipal, "user"))
	if err != nil || again.StoreID != first.ID() || again.ID != home.ID {
		t.Fatalf("existing Home moved with default = %+v, %v", again, err)
	}
	if _, err := r2.CutoverStore(context.Background(), home, second.ID(), "../escape", "worker"); err == nil {
		t.Fatal("cutover accepted escaping locator")
	}
	if _, err := r2.AcquireMaintenance(context.Background(), home.ID, "", time.Now().UTC().Add(time.Minute)); err == nil {
		t.Fatal("empty maintenance owner accepted")
	}
	if _, err := r2.AcquireMaintenance(context.Background(), home.ID, "worker", time.Now().UTC()); err == nil {
		t.Fatal("expired maintenance lease accepted")
	}
	lease, err := r2.AcquireMaintenance(context.Background(), home.ID, "worker", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	newLocator, err := second.Allocate(home.Key)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := r2.CutoverStore(context.Background(), home, second.ID(), newLocator, lease.Token)
	if err != nil || moved.StoreID != second.ID() || moved.Locator != newLocator {
		t.Fatalf("cutover = %+v, %v", moved, err)
	}
}

func TestCutoverStoreRequiresCurrentUnexpiredLeaseToken(t *testing.T) {
	db := dbtest.New(t)
	first, err := NewLocalStore("first", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalStore("second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, first.ID(), first, second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	record := mustEnsureHome(t, r, Principal(UserPrincipal, "maintenance-fence"))
	locator, err := second.Allocate(record.Key)
	if err != nil {
		t.Fatal(err)
	}
	lease1, err := r.AcquireMaintenance(ctx, record.ID, "worker", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE storage_home SET maintenance_until = now() - interval '1 second' WHERE id = $1", record.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseMaintenance(ctx, record.ID, lease1.Token); err == nil {
		t.Fatal("expired lease released as current")
	}
	if _, err := r.CutoverStore(ctx, record, second.ID(), locator, lease1.Token); err == nil {
		t.Fatal("expired lease cut over without takeover")
	}
	lease2, err := r.AcquireMaintenance(ctx, record.ID, "worker", time.Now().UTC().Add(time.Minute))
	if err != nil || lease2.Token == lease1.Token {
		t.Fatalf("same-owner reacquisition = %#v, %v", lease2, err)
	}
	if _, err := r.CutoverStore(ctx, record, second.ID(), locator, lease1.Token); err == nil {
		t.Fatal("stale same-owner token cut over")
	}
	if _, err := db.Exec(ctx, "UPDATE storage_home SET maintenance_until = now() - interval '1 second' WHERE id = $1", record.ID); err != nil {
		t.Fatal(err)
	}
	lease3, err := r.AcquireMaintenance(ctx, record.ID, "takeover", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CutoverStore(ctx, record, second.ID(), locator, lease2.Token); err == nil {
		t.Fatal("pre-takeover token cut over")
	}
	moved, err := r.CutoverStore(ctx, record, second.ID(), locator, lease3.Token)
	if err != nil || moved.StoreID != second.ID() {
		t.Fatalf("current takeover lease cutover = %#v, %v", moved, err)
	}
}

func TestTombstoneClearsMaintenanceLease(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	record := mustEnsureHome(t, r, Principal(UserPrincipal, "maintained-delete"))
	if _, err := r.AcquireMaintenance(ctx, record.ID, "worker", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tombstoned, err := r.Tombstone(ctx, record.Key, "admin")
	if err != nil || tombstoned.State != StateTombstoned {
		t.Fatalf("tombstone maintained Home = %#v, %v", tombstoned, err)
	}
	row, err := r.q.GetStorageHome(ctx, record.ID)
	if err != nil || row.MaintenanceOwner.Valid || row.MaintenanceToken.Valid || row.MaintenanceUntil.Valid {
		t.Fatalf("maintenance lease survived tombstone: %#v, %v", row, err)
	}
}

func TestStorageHomeStructuralConstraints(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		sql  string
	}{
		{name: "principal domain", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator) VALUES ('principal', 'tenant', 'id', 'store', 'locator')`},
		{name: "missing principal kind", sql: `INSERT INTO storage_home (home_kind, principal_id, store_id, locator) VALUES ('principal', 'id', 'store', 'locator')`},
		{name: "blank principal", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator) VALUES ('principal', 'user', ' ', 'store', 'locator')`},
		{name: "blank store", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator) VALUES ('principal', 'user', 'id', ' ', 'locator')`},
		{name: "terminal audit", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator, state) VALUES ('principal', 'user', 'id', 'store', 'locator', 'purged')`},
		{name: "partial maintenance", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator, maintenance_owner) VALUES ('principal', 'user', 'id', 'store', 'locator', 'worker')`},
		{name: "partial purge claim", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator, purge_claim_token) VALUES ('principal', 'user', 'id', 'store', 'locator', 'claim')`},
		{name: "ready purge audit", sql: `INSERT INTO storage_home (home_kind, principal_kind, principal_id, store_id, locator, state, purge_started_at) VALUES ('principal', 'user', 'id', 'store', 'locator', 'ready', now())`},
		{name: "migration completion", sql: `INSERT INTO storage_migration (name, state) VALUES ('bad-completion', 'completed')`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.Exec(ctx, tt.sql); err == nil {
				t.Fatal("durable malformed row was accepted")
			}
		})
	}
}

func TestCutoverStoreRejectsSharedSkillRootsUntilPhase2(t *testing.T) {
	db := dbtest.New(t)
	first, err := NewLocalStore("first", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalStore("second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, first.ID(), first, second)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []Key{SystemSkills(), SystemAgentSkills("agent")} {
		t.Run(string(key.Kind), func(t *testing.T) {
			record, err := r.Ensure(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			locator, err := second.Allocate(key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.CutoverStore(context.Background(), record, second.ID(), locator, "token"); err == nil || !strings.Contains(err.Error(), "Phase 2 Skill consumers") {
				t.Fatalf("shared Skill root cutover error = %v, want Phase 2 requirement", err)
			}
		})
	}
}

func TestCorruptRegistryRowsFailClosed(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	home, err := r.Ensure(ctx, Principal(UserPrincipal, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	// State sets stay Go-validated; direct corruption must never attach.
	if _, err := db.Exec(ctx, "UPDATE storage_home SET state = 'unknown', updated_at = now() WHERE id = $1", home.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, home.Key, false); err == nil {
		t.Fatal("unknown state resolved")
	}
	if _, err := r.Ensure(ctx, home.Key); err == nil {
		t.Fatal("unknown state ensured")
	}
	if _, err := db.Exec(ctx, "UPDATE storage_home SET state = 'ready', locator = '../escape', updated_at = now() WHERE id = $1", home.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, home.Key, false); err == nil {
		t.Fatal("escaping locator resolved")
	}
	if _, err := db.Exec(ctx, "UPDATE storage_home SET state = 'tombstoned', tombstoned_at = now(), tombstoned_by = 'admin', purge_requested_at = now() WHERE id = $1", home.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Purge(ctx, home.ID, "admin"); err == nil {
		t.Fatal("escaping locator purged")
	}
}

func TestObserveMutableAssetObjectAuthorityStoresNoConfigurationSecret(t *testing.T) {
	r, _ := newRegistry(t)
	if err := r.ObserveMutableAssetObjectAuthority(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	marker, err := r.q.GetStorageMigration(context.Background(), MutableAssetObjectAuthorityMigration)
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != "pending" || !marker.ObjectAuthorityConfigured || string(marker.Metadata) != "{}" {
		t.Fatalf("marker = %+v", marker)
	}
}

func TestObserveMutableAssetObjectAuthorityIsMonotonicBeforeCompletion(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatal(err)
	}
	marker, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil || marker.State != "pending" || !marker.ObjectAuthorityConfigured || marker.CompletedAt.Valid {
		t.Fatalf("second-start marker = %+v, %v", marker, err)
	}
}

func TestObserveMutableAssetObjectAuthorityDoesNotRegressCompleted(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: MutableAssetObjectAuthorityMigration, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatal(err)
	}
	marker, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil || marker.State != "completed" || !marker.ObjectAuthorityConfigured || !marker.CompletedAt.Valid || string(marker.Metadata) != "{}" {
		t.Fatalf("completed marker regressed: %+v, %v", marker, err)
	}
}

func TestRegisterLegacyUsesOnlyAuthoritativeIdentities(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The same raw UUID is a valid user and group identity, but must remain disjoint.
	principalID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", principalID, principalID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO ctx_group_state (id, platform, platform_group_id) VALUES ($1, 'test', $2)", principalID, "group-"+principalID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"current", "stale", "group-agent"} {
		if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace) VALUES ($1, $1, '/tmp')", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, "INSERT INTO auth_user_agent (user_id, agent_id) VALUES ($1, 'current')", principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel (id) VALUES ('reply')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO channel_group_member (group_id, agent_id, reply_channel_id) VALUES ($1, 'group-agent', 'reply')", principalID); err != nil {
		t.Fatal(err)
	}
	staleKey := Agent(UserPrincipal, principalID, "stale")
	staleLocator, err := store.Allocate(staleKey)
	if err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(store.base, filepath.FromSlash(staleLocator), "keep")
	if err := os.MkdirAll(filepath.Dir(staleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleFile, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(staleFile)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(store.base, "users", principalID, "agents", "unknown")
	if err := os.MkdirAll(unknown, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []Key{Principal(UserPrincipal, principalID), Principal(GroupPrincipal, principalID), Agent(UserPrincipal, principalID, "current"), staleKey, Agent(GroupPrincipal, principalID, "group-agent"), SystemSkills(), SystemAgentSkills("current")} {
		if _, err := r.Resolve(ctx, key, false); err != nil {
			t.Fatalf("registered %v: %v", key, err)
		}
	}
	if _, err := r.Resolve(ctx, Agent(UserPrincipal, principalID, "unknown"), false); err == nil {
		t.Fatal("unknown directory became an AgentHome")
	}
	after, err := os.Stat(staleFile)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("legacy inode changed: %v", err)
	}
}

func TestValidateConfiguredStoresFailsBeforeRegistration(t *testing.T) {
	db := dbtest.New(t)
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, store.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	home, err := r.Ensure(context.Background(), Principal(UserPrincipal, "user"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), "UPDATE storage_home SET store_id = 'missing', updated_at = now() WHERE id = $1", home.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateConfiguredStores(context.Background()); err == nil {
		t.Fatal("unknown referenced Store accepted at startup")
	}
}

func TestValidateConfiguredStoresAllowsPurgedRetiredStore(t *testing.T) {
	db := dbtest.New(t)
	oldStore, err := NewLocalStore("old", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRegistry, err := NewRegistry(db, oldStore.ID(), oldStore)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	home, err := oldRegistry.Ensure(ctx, Principal(UserPrincipal, "user"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldRegistry.Tombstone(ctx, home.Key, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := oldRegistry.Purge(ctx, home.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	newStore, err := NewLocalStore("new", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newRegistry, err := NewRegistry(db, newStore.ID(), newStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := newRegistry.ValidateConfiguredStores(ctx); err != nil {
		t.Fatalf("purged retired Store blocked startup: %v", err)
	}
}

func TestValidateConfiguredStoresRequiresTombstonedRetiredStore(t *testing.T) {
	db := dbtest.New(t)
	oldStore, err := NewLocalStore("old", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRegistry, err := NewRegistry(db, oldStore.ID(), oldStore)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	home, err := oldRegistry.Ensure(ctx, Principal(UserPrincipal, "user"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldRegistry.Tombstone(ctx, home.Key, "operator"); err != nil {
		t.Fatal(err)
	}
	newStore, err := NewLocalStore("new", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newRegistry, err := NewRegistry(db, newStore.ID(), newStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := newRegistry.ValidateConfiguredStores(ctx); err == nil {
		t.Fatal("tombstoned retired Store did not block startup")
	}
}

type failingLegacyStore struct {
	Store
	local *LocalStore
	err   error
	calls int
}

func (s *failingLegacyStore) LegacyAgentIDs(key Key) ([]string, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.local.LegacyAgentIDs(key)
}

func TestRegisterLegacyRetriesPendingAndSkipsCompletedInventory(t *testing.T) {
	db := dbtest.New(t)
	local, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := &failingLegacyStore{Store: local, local: local, err: errors.New("injected legacy inspection failure")}
	r, err := NewRegistry(db, local.ID(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(ctx); err == nil {
		t.Fatal("injected inspection failure completed registration")
	}
	marker, err := r.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	if err != nil || marker.State != "pending" || marker.CompletedAt.Valid {
		t.Fatalf("marker after failed registration = %+v, %v", marker, err)
	}
	store.err = nil
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	marker, err = r.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	var metadata map[string]string
	if err != nil || json.Unmarshal(marker.Metadata, &metadata) != nil || marker.State != "completed" || !marker.CompletedAt.Valid || metadata["store_id"] != "local" {
		t.Fatalf("marker after retry = %+v, %v", marker, err)
	}
	store.err = errors.New("completed registration must not inspect disk")
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatalf("completed registration re-inspected inventory: %v", err)
	}
}

func TestRegisterLegacyInspectsOnlyDefaultStore(t *testing.T) {
	db := dbtest.New(t)
	defaultStore, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldStore, err := NewLocalStore("old", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failingOldStore := &failingLegacyStore{Store: oldStore, local: oldStore, err: errors.New("non-default Store must not be inspected")}
	r, err := NewRegistry(db, defaultStore.ID(), defaultStore, failingOldStore)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	if _, err := db.Exec(context.Background(), "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterLegacyPendingMarkerUsesRecordedStore(t *testing.T) {
	db := dbtest.New(t)
	baseA, baseB := t.TempDir(), t.TempDir()
	storeA, err := NewLocalStore("store-a", baseA)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewLocalStore("store-b", baseB)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := sqlc.New(db).CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{"store_id":"store-a"}`)}); err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO agent (id, name, workspace) VALUES ('agent-a', 'Agent A', '/tmp')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO auth_user_agent (user_id, agent_id) VALUES ($1, 'agent-a')", userID); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(baseA, "users", userID, "agents", "agent-a", "keep")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	rB, err := NewRegistry(db, storeB.ID(), storeA, storeB)
	if err != nil {
		t.Fatal(err)
	}
	if err := rB.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, "SELECT store_id FROM storage_home")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var storeID string
		if err := rows.Scan(&storeID); err != nil {
			t.Fatal(err)
		}
		if storeID != storeA.ID() {
			t.Fatalf("adopted Home Store = %q, want marker Store A", storeID)
		}
	}
	if entries, err := os.ReadDir(baseB); err != nil || len(entries) != 0 {
		t.Fatalf("default Store B data = %v, %v; want none", entries, err)
	}
	marker, err := rB.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	var metadata map[string]string
	if err != nil || json.Unmarshal(marker.Metadata, &metadata) != nil || marker.State != "completed" || metadata["store_id"] != storeA.ID() {
		t.Fatalf("completed marker = %+v, %v", marker, err)
	}
}

func TestRegisterLegacyFailsWhenRecordedStoreIsAbsent(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	if _, err := sqlc.New(db).CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{"store_id":"store-a"}`)}); err != nil {
		t.Fatal(err)
	}
	storeB, err := NewLocalStore("store-b", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, storeB.ID(), storeB)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(ctx); err == nil {
		t.Fatal("registration accepted an absent recorded Store")
	}
}

func TestNewLocalRegistryReconstructsPendingMarkerStore(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	base := t.TempDir()
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{"store_id":"store-a"}`)}); err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	r, err := NewLocalRegistry(ctx, db, "store-b", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := q.GetPrincipalStorageHome(ctx, sqlc.GetPrincipalStorageHomeParams{PrincipalKind: text(string(UserPrincipal)), PrincipalID: text(userID)})
	if err != nil || record.StoreID != "store-a" {
		t.Fatalf("production registration Store = %#v, %v", record, err)
	}
}

func TestNewLocalRegistryRejectsMalformedPendingMarker(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	if _, err := sqlc.New(db).CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalRegistry(ctx, db, "local", t.TempDir()); err == nil {
		t.Fatal("malformed pending marker Store was serviceable")
	}
}

func TestNewLocalRegistryRetainsPurgedStoreForIdempotency(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	base := t.TempDir()
	oldStore, err := NewLocalStore("old", base)
	if err != nil {
		t.Fatal(err)
	}
	oldRegistry, err := NewRegistry(db, oldStore.ID(), oldStore)
	if err != nil {
		t.Fatal(err)
	}
	record := mustTombstoneHome(t, oldRegistry, Principal(UserPrincipal, "purged-old-store"))
	if _, err := oldRegistry.Purge(ctx, record.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	r, err := NewLocalRegistry(ctx, db, "new", base)
	if err != nil {
		t.Fatal(err)
	}
	purged, err := r.Purge(ctx, record.ID, "stale-worker")
	if err != nil || purged.State != StatePurged || purged.StoreID != "old" {
		t.Fatalf("idempotent retained-Store purge = %#v, %v", purged, err)
	}
}

func TestRegisterLegacyCompletedMarkerAllowsRetiredStore(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{"store_id":"old"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	newStore, err := NewLocalStore("new", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(db, newStore.ID(), newStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatalf("completed marker pinned retired Store: %v", err)
	}
}

func TestObserveMutableAssetObjectAuthorityRejectsCompletedLocalToObjectTransition(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: MutableAssetObjectAuthorityMigration, State: "not_required"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, true); err == nil {
		t.Fatal("completed local authority migration accepted object authority")
	}
	marker, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil || marker.State != "completed" || marker.ObjectAuthorityConfigured {
		t.Fatalf("terminal marker changed: %+v, %v", marker, err)
	}
}
