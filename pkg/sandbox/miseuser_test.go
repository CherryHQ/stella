package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMiseUserToolsDir(t *testing.T) {
	home := "/srv/.stella"
	cases := map[string]string{
		"u1":          filepath.Join(home, "users", "u1", ".mise-tools"),
		"group-42":    filepath.Join(home, "users", "group-42", ".mise-tools"),
		"":            "", // no user → fall back to the system tree
		"a/b":         "", // path traversal rejected
		"..":          "",
		"bad name":    "",
		"weird;rm-rf": "",
	}
	for key, want := range cases {
		if got := MiseUserToolsDir(home, key); got != want {
			t.Errorf("MiseUserToolsDir(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestEnsureUserMiseHome_SeedsSystemInstallsAsRelativeSymlinks(t *testing.T) {
	stellaHome := t.TempDir()
	// A builtin tool already installed in the shared system tree.
	sysGo := filepath.Join(MiseToolsDir(stellaHome), "installs", "go", "1.21.0", "bin")
	mustMkdirAll(t, sysGo)
	mustWriteFile(t, filepath.Join(sysGo, "go"), "binary")

	userDir := MiseUserToolsDir(stellaHome, "u1")
	if err := EnsureUserMiseHome(stellaHome, userDir); err != nil {
		t.Fatalf("EnsureUserMiseHome: %v", err)
	}

	// Standard subdirs exist.
	for _, sub := range []string{"installs", "shims", "cache", "state", "config"} {
		if _, err := os.Stat(filepath.Join(userDir, sub)); err != nil {
			t.Fatalf("missing per-user subdir %s: %v", sub, err)
		}
	}

	link := filepath.Join(userDir, "installs", "go", "1.21.0")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("seeded install should be a symlink: %v", err)
	}
	if filepath.IsAbs(target) {
		t.Fatalf("seed target must be relative (sandbox-valid), got %q", target)
	}
	// It resolves to the system binary through the relative link.
	if _, err := os.Stat(filepath.Join(link, "bin", "go")); err != nil {
		t.Fatalf("relative seed symlink does not resolve to system install: %v", err)
	}
}

func TestEnsureUserMiseHome_IdempotentAndDoesNotShadowUserInstall(t *testing.T) {
	stellaHome := t.TempDir()
	mustMkdirAll(t, filepath.Join(MiseToolsDir(stellaHome), "installs", "node", "20.0.0"))

	userDir := MiseUserToolsDir(stellaHome, "u1")
	// A real user-installed version sits alongside the (to-be-seeded) system one.
	userNode22 := filepath.Join(userDir, "installs", "node", "22.0.0")
	mustMkdirAll(t, userNode22)

	for i := range 2 {
		if err := EnsureUserMiseHome(stellaHome, userDir); err != nil {
			t.Fatalf("EnsureUserMiseHome call %d: %v", i, err)
		}
	}

	// System version seeded as a symlink.
	if fi, err := os.Lstat(filepath.Join(userDir, "installs", "node", "20.0.0")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("system node@20 should be a seeded symlink, lstat=%v err=%v", fi, err)
	}
	// The user's own version is left as a real dir, never replaced by a symlink.
	if fi, err := os.Lstat(userNode22); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("user node@22 must stay a real dir, lstat=%v err=%v", fi, err)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
