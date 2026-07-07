package blob

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

func TestNewStoreFromEnv(t *testing.T) {
	t.Setenv("STELLA_BLOB_S3_ENDPOINT", "")
	t.Setenv("STELLA_BLOB_S3_BUCKET", "")
	t.Setenv("STELLA_BLOB_S3_ACCESS_KEY", "")
	t.Setenv("STELLA_BLOB_S3_SECRET_KEY", "")
	store, err := NewStoreFromEnv()
	if err != nil || store != nil {
		t.Fatalf("all unset store=%T err=%v, want nil nil", store, err)
	}
	t.Setenv("STELLA_BLOB_S3_ENDPOINT", "localhost:9000")
	if store, err := NewStoreFromEnv(); err == nil || store != nil {
		t.Fatalf("partial store=%T err=%v, want error", store, err)
	}
}
