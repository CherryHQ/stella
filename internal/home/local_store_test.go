package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func localRecord(t *testing.T, store *LocalStore, id string, key Key) Record {
	t.Helper()
	locator, err := store.Allocate(key)
	if err != nil {
		t.Fatal(err)
	}
	return Record{ID: id, Key: key, StoreID: store.ID(), Locator: locator, State: StateReady}
}

func TestLocalStoreConfiguredBaseAndReadyRootsFailClosed(t *testing.T) {
	parent, realBase := t.TempDir(), filepath.Join(t.TempDir(), "real")
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
	principal := localRecord(t, store, "p", Principal(UserPrincipal, "u"))
	agent := localRecord(t, store, "a", Agent(UserPrincipal, "u", "a"))
	for _, home := range []Record{principal, agent} {
		if err := store.Ensure(context.Background(), home); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(store.base, filepath.FromSlash(agent.Locator))); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareWorkspace(principal, agent); err == nil {
		t.Fatal("workspace recreated missing ready root")
	}
	outside := t.TempDir()
	root := filepath.Join(store.base, filepath.FromSlash(principal.Locator))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := store.InspectReadyRoot(principal); err == nil {
		t.Fatal("accepted symlinked ready root")
	}
	if _, err := os.Stat(filepath.Join(outside, "workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrote outside root: %v", err)
	}
}

func TestLocalStoreReadyRootPinRejectsReplacement(t *testing.T) {
	store, _ := NewLocalStore("local", t.TempDir())
	home := localRecord(t, store, "p", Principal(UserPrincipal, "replace"))
	if err := store.Ensure(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	pin, err := store.PinReadyRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close() //nolint:errcheck
	root := filepath.Join(store.base, filepath.FromSlash(home.Locator))
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Skipf("cannot replace open directory: %v", err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pin.Revalidate(); err == nil {
		t.Fatal("pin accepted replacement")
	}
}
