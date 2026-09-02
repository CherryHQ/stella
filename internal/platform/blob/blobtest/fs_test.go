package blobtest

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFSStorePutOpenDelete(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "users/u/data/assets/202607/a.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := store.Open(ctx, "users/u/data/assets/202607/a.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadAll = %q, %v", data, err)
	}
	if err := store.Delete(ctx, "users/u/data/assets/202607/a.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Open(ctx, "users/u/data/assets/202607/a.txt"); !os.IsNotExist(err) {
		t.Fatalf("Open after delete err=%v, want not exist", err)
	}
}

func TestFSStoreRejectsTraversal(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../x", "users/../x", "/abs", ""} {
		if err := store.Put(context.Background(), key, strings.NewReader("x")); err == nil {
			t.Fatalf("Put(%q) succeeded, want error", key)
		}
	}
}

func TestFSStorePutLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	store, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	key := "users/u/data/assets/a.txt"
	if err := store.Put(context.Background(), key, strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dir := filepath.Join(root, "users", "u", "data", "assets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stella-blob-") {
			t.Fatalf("temp file %q left after atomic rename", entry.Name())
		}
	}
	info, err := os.Stat(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestFSStoreList(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Nested keys under the prefix, plus one sibling outside it that must not leak.
	seed := map[string]string{
		"users/u/data/assets/202607/a.txt": "a",
		"users/u/data/assets/202608/b.txt": "b",
		"users/u/data/assets/c.txt":        "c",
		"users/u/data/other/d.txt":         "d",
	}
	for k, v := range seed {
		if err := store.Put(ctx, k, strings.NewReader(v)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	keys, err := store.List(ctx, "users/u/data/assets")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}
	want := []string{
		"users/u/data/assets/202607/a.txt",
		"users/u/data/assets/202608/b.txt",
		"users/u/data/assets/c.txt",
	}
	if len(keys) != len(want) {
		t.Fatalf("List returned %d keys (%v), want %d", len(keys), keys, len(want))
	}
	for _, k := range want {
		if !got[k] {
			t.Fatalf("List missing key %q; got %v", k, keys)
		}
	}
	if got["users/u/data/other/d.txt"] {
		t.Fatalf("List leaked sibling key outside prefix; got %v", keys)
	}
}

func TestFSStoreListMissingDir(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(context.Background(), "users/nobody/data/assets")
	if err != nil {
		t.Fatalf("List missing dir err=%v, want nil", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List missing dir = %v, want empty", keys)
	}
}

func TestFSStoreListRejectsTraversal(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"../x", "users/../../x", "/abs", ""} {
		if _, err := store.List(context.Background(), prefix); err == nil {
			t.Fatalf("List(%q) succeeded, want error", prefix)
		}
	}
}
