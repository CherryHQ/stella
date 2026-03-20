package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePathWithinAllowed(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ValidatePath(dir, filepath.Join(sub, "file.txt")); err != nil {
		t.Errorf("expected path within dir to be allowed: %v", err)
	}
}

func TestValidatePathOutside(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()

	err := ValidatePath(dir, filepath.Join(other, "file.txt"))
	if err == nil {
		t.Error("expected error for path outside allowed dir")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("expected sandbox error, got: %v", err)
	}
}

func TestValidatePathTraversal(t *testing.T) {
	dir := t.TempDir()
	// Attempt directory traversal.
	traversal := filepath.Join(dir, "..", filepath.Base(dir)+"x", "evil.txt")
	err := ValidatePath(dir, traversal)
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestValidatePathEmptyAllowedDir(t *testing.T) {
	// Empty allowed dir means no sandbox — should always pass.
	if err := ValidatePath("", "/any/path/file.txt"); err != nil {
		t.Errorf("expected nil for empty allowed dir: %v", err)
	}
}

func TestValidatePathExactMatch(t *testing.T) {
	dir := t.TempDir()
	// The allowed dir itself should be allowed.
	if err := ValidatePath(dir, dir); err != nil {
		t.Errorf("allowed dir itself should be allowed: %v", err)
	}
}

func TestValidatePathSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()

	// Create a symlink inside dir that points outside.
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	err := ValidatePath(dir, filepath.Join(link, "file.txt"))
	if err == nil {
		t.Error("expected error for symlink escape")
	}
}

func TestValidatePathNewFile(t *testing.T) {
	dir := t.TempDir()
	// File doesn't exist yet but is within allowed dir.
	newFile := filepath.Join(dir, "newdir", "newfile.txt")
	if err := ValidatePath(dir, newFile); err != nil {
		t.Errorf("new file within allowed dir should be allowed: %v", err)
	}
}

func TestValidatePathPrefixConfusion(t *testing.T) {
	// Ensure "allowed-dir-suffix" doesn't match "allowed-dir".
	base := t.TempDir()
	dir1 := filepath.Join(base, "data")
	dir2 := filepath.Join(base, "data-evil")
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}

	err := ValidatePath(dir1, filepath.Join(dir2, "file.txt"))
	if err == nil {
		t.Error("expected error: dir2 is not inside dir1 despite sharing a prefix")
	}
}
