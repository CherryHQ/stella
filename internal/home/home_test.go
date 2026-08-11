package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

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

func TestTypedIdentityConcurrentAdoptionAndTombstone(t *testing.T) {
	r, store := newRegistry(t)
	ctx := context.Background()
	user, err := r.Ensure(ctx, Principal(UserPrincipal, "same"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := r.Ensure(ctx, Principal(GroupPrincipal, "same"))
	if err != nil || user.ID == group.ID || user.Locator == group.Locator {
		t.Fatalf("typed isolation: %#v %#v %v", user, group, err)
	}
	key := Principal(UserPrincipal, "legacy")
	locator, _ := store.Allocate(key)
	file := filepath.Join(store.base, filepath.FromSlash(locator), "keep")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	var wg sync.WaitGroup
	results := make(chan Record, 2)
	for range 2 {
		wg.Go(func() {
			h, e := r.Ensure(ctx, key)
			if e != nil {
				t.Error(e)
				return
			}
			results <- h
		})
	}
	wg.Wait()
	close(results)
	var first string
	for h := range results {
		if first == "" {
			first = h.ID
		} else if h.ID != first {
			t.Fatal("concurrent Ensure split identity")
		}
	}
	after, err := os.Stat(file)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("legacy bytes copied: %v", err)
	}
	if _, err := r.Tombstone(ctx, key, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ensure(ctx, key); err == nil {
		t.Fatal("tombstone ensured")
	}
	if _, err := r.Resolve(ctx, key, false); err == nil {
		t.Fatal("tombstone resolved")
	}
}

func TestReadyRootsMissingAndSymlinkFailClosedForAllKinds(t *testing.T) {
	keys := []Key{Principal(UserPrincipal, "u"), Agent(UserPrincipal, "u", "a"), SystemSkills(), SystemAgentSkills("a")}
	for _, key := range keys {
		t.Run(string(key.Kind), func(t *testing.T) {
			r, store := newRegistry(t)
			home, err := r.Ensure(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(store.base, filepath.FromSlash(home.Locator))
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Ensure(context.Background(), key); err == nil {
				t.Fatal("missing ready root recreated")
			}
			if err := os.Symlink(t.TempDir(), root); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if _, err := r.Resolve(context.Background(), key, false); err == nil {
				t.Fatal("symlink ready root resolved")
			}
		})
	}
}

func TestObjectAuthorityObservationIsMonotonicAcrossStarts(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	if err := r.ObserveMutableAssetObjectAuthority(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveMutableAssetObjectAuthority(ctx, false); err != nil {
		t.Fatal(err)
	}
	marker, err := r.q.GetStorageMigration(ctx, MutableAssetObjectAuthorityMigration)
	if err != nil || !marker.ObjectAuthorityConfigured || marker.State != "pending" {
		t.Fatalf("marker regressed: %#v %v", marker, err)
	}
}

func TestLegacyRegistrationAdoptsWithoutCopyAndPersistsConfiguredStore(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	base := t.TempDir()
	store, _ := NewLocalStore("store-a", base)
	r, _ := NewRegistry(db, store.ID(), store)
	userID := uuid.NewString()
	if _, err := db.Exec(ctx, "INSERT INTO auth_user (id,email) VALUES ($1,$2)", userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "users", userID, "keep")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	if err := r.RegisterLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(file)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("registration copied data: %v", err)
	}
	marker, err := r.q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	if err != nil || marker.State != "completed" {
		t.Fatalf("registration marker: %#v %v", marker, err)
	}
	home, err := r.Resolve(ctx, Principal(UserPrincipal, userID), false)
	if err != nil || home.StoreID != "store-a" {
		t.Fatalf("registered Home: %#v %v", home, err)
	}
	storeBBase := t.TempDir()
	if _, err := NewLocalRegistry(ctx, db, "store-b", storeBBase); err == nil {
		t.Fatal("persisted Store mismatch accepted")
	}
	if entries, err := os.ReadDir(storeBBase); err != nil || len(entries) != 0 {
		t.Fatalf("Store B created data: %v %v", entries, err)
	}
}

func TestLegacyPendingMarkerStoreMismatchFailsWithoutCreation(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{"store_id":"store-a"}`)}); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if _, err := NewLocalRegistry(ctx, db, "store-b", base); err == nil {
		t.Fatal("pending Store mismatch accepted")
	}
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Fatalf("mismatch created Store B: %v %v", entries, err)
	}
	marker, err := q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	if err != nil || marker.State != "pending" || marker.CompletedAt.Valid {
		t.Fatalf("pending marker changed: %#v %v", marker, err)
	}
	if _, err := q.GetPrincipalStorageHome(ctx, sqlc.GetPrincipalStorageHomeParams{}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unexpected Home: %v", err)
	}
}

func TestLegacyCompletedMarkerStoreMismatchFailsWithoutCreation(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	q := sqlc.New(db)
	if _, err := q.CreateStorageMigration(ctx, sqlc.CreateStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, Metadata: []byte(`{"store_id":"store-a"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CompleteStorageMigration(ctx, sqlc.CompleteStorageMigrationParams{Name: TypedHomeLegacyRegistrationMigration, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if _, err := NewLocalRegistry(ctx, db, "store-b", base); err == nil {
		t.Fatal("completed Store mismatch accepted")
	}
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Fatalf("mismatch created Store B: %v %v", entries, err)
	}
}
