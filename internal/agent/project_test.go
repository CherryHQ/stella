package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProjectDir(t *testing.T) {
	tmp := t.TempDir()
	userRoot := filepath.Join(tmp, "users", "alice")
	subDir := filepath.Join(userRoot, "projects", "myapp")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		baseDir string
		wantErr bool
	}{
		{"equal to userRoot", userRoot, false},
		{"subpath", subDir, false},
		{"deeper subpath", filepath.Join(subDir, "deep", "nested"), false},
		{"parent traversal", filepath.Join(userRoot, "..", "bob"), true},
		{"absolute outside", "/etc/passwd", true},
		{"relative path", "projects/myapp", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectDir(tt.baseDir, userRoot)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProjectDir(%q, %q) error = %v, wantErr %v", tt.baseDir, userRoot, err, tt.wantErr)
			}
		})
	}
}

func TestValidateProjectDir_SymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	userRoot := filepath.Join(tmp, "users", "alice")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a target outside userRoot.
	outsideDir := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Symlink inside userRoot pointing outside.
	symlink := filepath.Join(userRoot, "escape")
	if err := os.Symlink(outsideDir, symlink); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	err := ValidateProjectDir(symlink, userRoot)
	if err == nil {
		t.Error("expected error for symlink escaping userRoot, got nil")
	}
}
