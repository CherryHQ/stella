package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyExecutableReplacesSymlinkWithRegularFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	dest := filepath.Join(dir, "anna")

	if err := os.WriteFile(source, []byte("new anna"), 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(target, []byte("old anna"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, dest); err != nil {
		t.Fatalf("symlink dest: %v", err)
	}

	if err := copyExecutable(source, dest); err != nil {
		t.Fatalf("copyExecutable: %v", err)
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
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "new anna" {
		t.Fatalf("dest content = %q, want new anna", got)
	}
}
