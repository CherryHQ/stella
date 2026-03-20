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

func TestSetupUserWorkspace(t *testing.T) {
	base := t.TempDir()
	userDir, err := SetupUserWorkspace("agent-1", base, 42)
	if err != nil {
		t.Fatalf("SetupUserWorkspace: %v", err)
	}

	want := filepath.Join(base, "workspaces", "agent-1", "users", "42")
	if userDir != want {
		t.Errorf("dir = %q, want %q", userDir, want)
	}

	// Verify subdirectories.
	for _, sub := range []string{
		filepath.Join(userDir, ".agents", "skills"),
		filepath.Join(userDir, "data"),
	} {
		info, err := os.Stat(sub)
		if err != nil {
			t.Errorf("dir %q not created: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", sub)
		}
	}
}

func TestSetupUserWorkspaceEmptyAgent(t *testing.T) {
	_, err := SetupUserWorkspace("", t.TempDir(), 1)
	if err == nil {
		t.Error("expected error for empty agent ID")
	}
}

func TestSetupUserWorkspaceInvalidUser(t *testing.T) {
	for _, id := range []int64{0, -1, -100} {
		_, err := SetupUserWorkspace("agent-1", t.TempDir(), id)
		if err == nil {
			t.Errorf("expected error for user ID %d", id)
		}
	}
}

func TestSetupUserWorkspaceIdempotent(t *testing.T) {
	base := t.TempDir()
	d1, err := SetupUserWorkspace("agent-1", base, 42)
	if err != nil {
		t.Fatal(err)
	}
	// Create a file to verify it survives second setup.
	testFile := filepath.Join(d1, "data", "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	d2, err := SetupUserWorkspace("agent-1", base, 42)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("paths differ: %q vs %q", d1, d2)
	}
	if _, err := os.Stat(testFile); err != nil {
		t.Error("file disappeared after second setup")
	}
}

func TestSetupUserWorkspaceIsolation(t *testing.T) {
	base := t.TempDir()
	d1, err := SetupUserWorkspace("agent-1", base, 1)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := SetupUserWorkspace("agent-1", base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Error("different users should have different paths")
	}

	// Write to user 1 data, verify user 2 doesn't see it.
	if err := os.WriteFile(filepath.Join(d1, "data", "secret.txt"), []byte("u1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d2, "data", "secret.txt")); !os.IsNotExist(err) {
		t.Error("user 2 should not see user 1's file")
	}
}

func TestUserSkillsDir(t *testing.T) {
	got := UserSkillsDir("/base/users/42")
	want := filepath.Join("/base/users/42", ".agents", "skills")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUserDataDir(t *testing.T) {
	got := UserDataDir("/base/users/42")
	want := filepath.Join("/base/users/42", "data")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
