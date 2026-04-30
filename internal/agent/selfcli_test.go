package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureAnnaCLIInPathReplacesSymlink(t *testing.T) {
	// Build a fake annaHome with bin/ containing a symlink where the binary should go.
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place a symlink at the destination to confirm it gets replaced by a regular file.
	target := filepath.Join(dir, "old-anna")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(binDir, "anna")
	if err := os.Symlink(target, dest); err != nil {
		t.Fatal(err)
	}

	if err := EnsureAnnaCLIInPath(dir); err != nil {
		t.Fatalf("EnsureAnnaCLIInPath: %v", err)
	}

	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("lstat dest: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("dest is still a symlink")
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("dest mode = %v, want regular file", info.Mode())
	}
}
