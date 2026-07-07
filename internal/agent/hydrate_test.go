package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/blob"
)

// countingStore wraps a Store to count Open/List calls, so a test can assert
// single-flight (a second hydration touches the mirror zero more times).
type countingStore struct {
	inner blob.Store
	opens int
	lists int
}

func (c *countingStore) Put(ctx context.Context, key string, r io.Reader) error {
	return c.inner.Put(ctx, key, r)
}

func (c *countingStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	c.opens++
	return c.inner.Open(ctx, key)
}

func (c *countingStore) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}

func (c *countingStore) List(ctx context.Context, prefix string) ([]string, error) {
	c.lists++
	return c.inner.List(ctx, prefix)
}

// putBlob seeds a key into the mirror.
func putBlob(t *testing.T, store blob.Store, key, content string) {
	t.Helper()
	if err := store.Put(context.Background(), key, strings.NewReader(content)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func TestHydrateUserAssetsRestoresAndSkips(t *testing.T) {
	blob.ResetDefaultForTest()
	defer blob.ResetDefaultForTest()
	resetHydrationForTest()

	stellaHome := t.TempDir()
	store, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The mirror holds two assets; one already exists locally with different
	// content and must not be overwritten.
	putBlob(t, store, "users/u1/data/assets/202607/missing.txt", "remote-missing")
	putBlob(t, store, "users/u1/data/assets/existing.txt", "remote-existing")
	if err := blob.SetDefault(store); err != nil {
		t.Fatal(err)
	}

	userHome := UserHomeDir(stellaHome, "u1")
	existingAbs := filepath.Join(UserAssetsDir(userHome), "existing.txt")
	if err := os.MkdirAll(filepath.Dir(existingAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingAbs, []byte("local-existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := HydrateUserAssets(context.Background(), stellaHome, userHome); err != nil {
		t.Fatalf("HydrateUserAssets: %v", err)
	}

	// Missing file restored from the mirror at 0644.
	missingAbs := filepath.Join(UserAssetsDir(userHome), "202607", "missing.txt")
	data, err := os.ReadFile(missingAbs)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != "remote-missing" {
		t.Fatalf("restored content = %q, want %q", data, "remote-missing")
	}
	info, err := os.Stat(missingAbs)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("restored mode = %v, want 0644", info.Mode().Perm())
	}

	// Existing local file untouched.
	data, err = os.ReadFile(existingAbs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "local-existing" {
		t.Fatalf("existing content = %q, want it untouched", data)
	}
}

// A file written between the caller's missing-file check and the install (the
// stat-then-restore window) must win over the restored blob content.
func TestRestoreAssetKeyDoesNotReplaceConcurrentWrite(t *testing.T) {
	stellaHome := t.TempDir()
	store, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "users/u1/data/assets/raced.txt"
	putBlob(t, store, key, "stale-remote")

	abs := filepath.Join(stellaHome, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("fresh-local"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restoreAssetKey(context.Background(), store, key, abs); err != nil {
		t.Fatalf("restoreAssetKey: %v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh-local" {
		t.Fatalf("content = %q, want the concurrent local write to win", data)
	}
	// The temp file must not linger next to the target.
	entries, err := os.ReadDir(filepath.Dir(abs))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stella-hydrate-") {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}
}

func TestHydrateUserAssetsNilStore(t *testing.T) {
	blob.ResetDefaultForTest()
	resetHydrationForTest()

	stellaHome := t.TempDir()
	userHome := UserHomeDir(stellaHome, "u1")
	if err := HydrateUserAssets(context.Background(), stellaHome, userHome); err != nil {
		t.Fatalf("nil store HydrateUserAssets err=%v, want nil", err)
	}
	if _, err := os.Stat(UserAssetsDir(userHome)); !os.IsNotExist(err) {
		t.Fatalf("nil store created assets dir; err=%v", err)
	}
}

func TestHydrateUserAssetsSingleFlight(t *testing.T) {
	blob.ResetDefaultForTest()
	defer blob.ResetDefaultForTest()
	resetHydrationForTest()

	stellaHome := t.TempDir()
	inner, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	putBlob(t, inner, "users/u2/data/assets/a.txt", "one")
	putBlob(t, inner, "users/u2/data/assets/b.txt", "two")
	counting := &countingStore{inner: inner}
	if err := blob.SetDefault(counting); err != nil {
		t.Fatal(err)
	}

	userHome := UserHomeDir(stellaHome, "u2")
	if err := HydrateUserAssets(context.Background(), stellaHome, userHome); err != nil {
		t.Fatalf("first HydrateUserAssets: %v", err)
	}
	opensAfterFirst, listsAfterFirst := counting.opens, counting.lists
	if opensAfterFirst != 2 {
		t.Fatalf("first call opens = %d, want 2", opensAfterFirst)
	}

	// Second call is a no-op: single-flight marks the home done for the process.
	if err := HydrateUserAssets(context.Background(), stellaHome, userHome); err != nil {
		t.Fatalf("second HydrateUserAssets: %v", err)
	}
	if counting.opens != opensAfterFirst || counting.lists != listsAfterFirst {
		t.Fatalf("second call touched store: opens %d->%d, lists %d->%d",
			opensAfterFirst, counting.opens, listsAfterFirst, counting.lists)
	}
}

func TestHydrateUserAssetsGroupHome(t *testing.T) {
	blob.ResetDefaultForTest()
	defer blob.ResetDefaultForTest()
	resetHydrationForTest()

	stellaHome := t.TempDir()
	store, err := blob.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Group homes live under users/group-{id}; the prefix is derived from the
	// home path, so hydration reaches group assets without hardcoding users/.
	putBlob(t, store, "users/group-g1/data/assets/x.txt", "group-asset")
	if err := blob.SetDefault(store); err != nil {
		t.Fatal(err)
	}

	groupHome := GroupHomeDir(stellaHome, "g1")
	if err := HydrateUserAssets(context.Background(), stellaHome, groupHome); err != nil {
		t.Fatalf("HydrateUserAssets group: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(UserAssetsDir(groupHome), "x.txt"))
	if err != nil {
		t.Fatalf("read restored group asset: %v", err)
	}
	if string(data) != "group-asset" {
		t.Fatalf("group asset content = %q, want %q", data, "group-asset")
	}
}
