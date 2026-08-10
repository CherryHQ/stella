package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func localRecord(t *testing.T, store *LocalStore, id string, key Key) Record {
	t.Helper()
	locator, err := store.Allocate(key)
	if err != nil {
		t.Fatal(err)
	}
	return Record{ID: id, Key: key, StoreID: store.ID(), Locator: locator, State: StateReady}
}

func TestLocalStorePurgeLockExcludesIndependentlyOpenedStore(t *testing.T) {
	base := t.TempDir()
	first, err := NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalStore("local", base)
	if err != nil {
		t.Fatal(err)
	}
	home := localRecord(t, first, "same-home", Principal(UserPrincipal, "u"))
	release, err := first.AcquirePurgeLock(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.AcquirePurgeLock(ctx, home); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contending lock error = %v, want deadline", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = second.AcquirePurgeLock(context.Background(), home)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalStoreAcceptsSymlinkedConfiguredBase(t *testing.T) {
	parent := t.TempDir()
	realBase := filepath.Join(parent, "real")
	if err := os.Mkdir(realBase, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(realBase, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := NewLocalStore("local", alias)
	if err != nil {
		t.Fatal(err)
	}
	home := localRecord(t, store, "p", Principal(UserPrincipal, "symlink-base"))
	if err := store.Ensure(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if err := store.InspectReadyRoot(home); err != nil {
		t.Fatalf("inspect through canonicalized base: %v", err)
	}
	if err := store.Purge(context.Background(), home); err != nil {
		t.Fatalf("purge through canonicalized base: %v", err)
	}
}

func TestLocalStoreReadyRootsFailClosed(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	principal := localRecord(t, store, "p", Principal(UserPrincipal, "u"))
	agent := localRecord(t, store, "a", Agent(UserPrincipal, "u", "a"))
	if err := store.Ensure(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if err := store.Ensure(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if err := store.InspectReadyRoot(principal); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(store.base, filepath.FromSlash(agent.Locator))); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareWorkspace(principal, agent); err == nil {
		t.Fatal("PrepareWorkspace recreated a missing ready agent root")
	}
	if _, err := os.Stat(filepath.Join(store.base, filepath.FromSlash(agent.Locator))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent root was recreated: %v", err)
	}

	outside := t.TempDir()
	principalPath := filepath.Join(store.base, filepath.FromSlash(principal.Locator))
	if err := os.RemoveAll(principalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, principalPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := store.InspectReadyRoot(principal); err == nil {
		t.Fatal("ready-root inspection accepted a symlink replacement")
	}
}

func TestLocalStoreReadyRootPinRejectsDirectoryReplacement(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := localRecord(t, store, "p", Principal(UserPrincipal, "replace"))
	if err := store.Ensure(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	pin, err := store.PinReadyRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close() //nolint:errcheck
	physical := filepath.Join(store.base, filepath.FromSlash(home.Locator))
	moved := physical + "-moved"
	if err := os.Rename(physical, moved); err != nil {
		t.Skipf("cannot replace an opened directory on this platform: %v", err)
	}
	if err := os.Mkdir(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pin.Revalidate(); err == nil {
		t.Fatal("ready-root pin accepted a different directory at the persisted locator")
	}
}

func TestLocalStorePurgeHonorsCanceledContextAndIsIdempotent(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := localRecord(t, store, "p", Principal(UserPrincipal, "u"))
	if err := store.Ensure(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Purge(ctx, home); !errors.Is(err, context.Canceled) {
		t.Fatalf("Purge error = %v, want canceled", err)
	}
	if err := store.Purge(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if err := store.Purge(context.Background(), home); err != nil {
		t.Fatalf("idempotent Purge: %v", err)
	}
}

func TestLocalStorePurgeDoesNotFollowSiblingHomeSymlink(t *testing.T) {
	store, err := NewLocalStore("local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := localRecord(t, store, "target", Principal(UserPrincipal, "target"))
	victim := localRecord(t, store, "victim", Principal(UserPrincipal, "victim"))
	for _, home := range []Record{target, victim} {
		if err := store.Ensure(context.Background(), home); err != nil {
			t.Fatal(err)
		}
	}
	victimFile := filepath.Join(store.base, filepath.FromSlash(victim.Locator), "keep")
	if err := os.WriteFile(victimFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(store.base, filepath.FromSlash(target.Locator))
	if err := os.RemoveAll(targetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "victim"), targetPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := store.Purge(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(victimFile); err != nil || string(data) != "keep" {
		t.Fatalf("sibling Home changed through purge symlink: %q, %v", data, err)
	}
}
