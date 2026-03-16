package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetupWorkspace(t *testing.T) {
	base := t.TempDir()

	dir, err := SetupWorkspace("anna", base)
	if err != nil {
		t.Fatalf("SetupWorkspace: %v", err)
	}

	want := filepath.Join(base, "workspaces", "anna")
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}

	// Verify the skills subdirectory was created.
	skillsDir := filepath.Join(dir, "skills")
	info, err := os.Stat(skillsDir)
	if err != nil {
		t.Fatalf("skills dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("skills path is not a directory")
	}
}

func TestSetupWorkspaceIdempotent(t *testing.T) {
	base := t.TempDir()

	dir1, err := SetupWorkspace("anna", base)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	dir2, err := SetupWorkspace("anna", base)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if dir1 != dir2 {
		t.Errorf("dirs differ: %q vs %q", dir1, dir2)
	}
}

func TestSetupWorkspaceEmptyID(t *testing.T) {
	_, err := SetupWorkspace("", t.TempDir())
	if err == nil {
		t.Error("expected error for empty agent ID")
	}
}
