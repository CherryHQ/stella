package home

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

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
	flaky.fail = false
	purged, err := r.Purge(ctx, user.ID, "admin")
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
	for err := range errs {
		t.Fatal(err)
	}
	for record := range results {
		if record.State != StatePurged {
			t.Fatalf("concurrent purge state = %s", record.State)
		}
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
	if _, err := r2.AcquireMaintenance(context.Background(), home.ID, "worker", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	newLocator, err := second.Allocate(home.Key)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := r2.CutoverStore(context.Background(), home, second.ID(), newLocator, "worker")
	if err != nil || moved.StoreID != second.ID() || moved.Locator != newLocator {
		t.Fatalf("cutover = %+v, %v", moved, err)
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
	if _, err := db.Exec(ctx, "UPDATE storage_home SET state = 'tombstoned' WHERE id = $1", home.ID); err != nil {
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
	storeA, err := NewLocalStore("store-a", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewLocalStore("store-b", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &failingLegacyStore{Store: storeA, local: storeA, err: errors.New("fail Store A inspection")}
	rA, err := NewRegistry(db, storeA.ID(), a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id, email) VALUES ($1, $2)", userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := rA.RegisterLegacy(ctx); err == nil {
		t.Fatal("Store A failure did not leave a pending marker")
	}
	if a.calls == 0 {
		t.Fatal("initial registration did not inspect Store A")
	}
	a.err = nil
	b := &failingLegacyStore{Store: storeB, local: storeB, err: errors.New("retry inspected new default Store B")}
	rB, err := NewRegistry(db, storeB.ID(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := rB.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if b.calls != 0 || a.calls < 2 {
		t.Fatalf("retry Store calls: A=%d B=%d, want A only", a.calls, b.calls)
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
