package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMarkNativeCleanupPendingIsIdempotent(t *testing.T) {
	home := t.TempDir()
	if err := MarkNativeCleanupPending(home, "session-1", "local"); err != nil {
		t.Fatal(err)
	}
	if err := MarkNativeCleanupPending(home, "session-1", "local"); err != nil {
		t.Fatal(err)
	}
	allowed, err := CleanupAllowed(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("native marker did not block cleanup")
	}
	entries, err := os.ReadDir(filepath.Join(home, nativeCleanupMarkerDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "session-1.json" {
		t.Fatalf("marker entries = %v, want one durable marker", entries)
	}
}

func TestCleanupAllowedFailClosedOnStagingEntry(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, nativeCleanupMarkerDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pending-crash"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed, err := CleanupAllowed(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("staging entry did not block cleanup")
	}
}
