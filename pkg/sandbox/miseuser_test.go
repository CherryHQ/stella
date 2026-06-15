package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMiseUserToolsDir(t *testing.T) {
	home := "/srv/.stella"
	type in struct{ dir, id string }
	cases := map[in]string{
		{"users", "u1"}:          filepath.Join(home, "users", "u1", ".mise-tools"),
		{"users", "group-42"}:    filepath.Join(home, "users", "group-42", ".mise-tools"), // a channel group, prefixed
		{"users", ""}:            "",                                                      // no id → fall back to the system tree
		{"", "u1"}:               "",                                                      // no principal subtree
		{"groups", "42"}:         "",                                                      // groups no longer a top-level subtree (#442)
		{"vault", "u1"}:          "",                                                      // unknown subtree rejected
		{"users", "a/b"}:         "",                                                      // path traversal rejected
		{"users", ".."}:          "",
		{"users", "bad name"}:    "",
		{"users", "weird;rm-rf"}: "",
	}
	for k, want := range cases {
		if got := MiseUserToolsDir(home, k.dir, k.id); got != want {
			t.Errorf("MiseUserToolsDir(%q, %q) = %q, want %q", k.dir, k.id, got, want)
		}
	}
}

func TestPerUserMiseDataDir(t *testing.T) {
	home := "/srv/.stella"
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"per-user tree": {map[string]string{"MISE_DATA_DIR": home + "/users/u1/.mise-tools"}, home + "/users/u1/.mise-tools"},
		"system tree":   {map[string]string{"MISE_DATA_DIR": MiseToolsDir(home)}, ""},
		"unset":         {map[string]string{}, ""},
	}
	for name, tc := range cases {
		if got := PerUserMiseDataDir(tc.env, home); got != tc.want {
			t.Errorf("%s: PerUserMiseDataDir = %q, want %q", name, got, tc.want)
		}
	}
}

func TestEnsureUserMiseHome_SeedsSystemInstallsAsRelativeSymlinks(t *testing.T) {
	stellaHome := t.TempDir()
	// A builtin tool already installed in the shared system tree.
	sysGo := filepath.Join(MiseToolsDir(stellaHome), "installs", "go", "1.21.0", "bin")
	mustMkdirAll(t, sysGo)
	mustWriteFile(t, filepath.Join(sysGo, "go"), "binary")

	userDir := MiseUserToolsDir(stellaHome, "users", "u1")
	if err := EnsureUserMiseHome(stellaHome, userDir); err != nil {
		t.Fatalf("EnsureUserMiseHome: %v", err)
	}

	// Standard subdirs exist.
	for _, sub := range []string{"installs", "shims", "cache", "state", "config"} {
		if _, err := os.Stat(filepath.Join(userDir, sub)); err != nil {
			t.Fatalf("missing per-user subdir %s: %v", sub, err)
		}
	}

	// The tree is private (0700) so other host users can't read a tenant's tools.
	if fi, err := os.Stat(userDir); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("per-user mise tree must not be group/other-accessible, mode=%v err=%v", fi.Mode().Perm(), err)
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

	userDir := MiseUserToolsDir(stellaHome, "users", "u1")
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

func TestEnsureUserMiseHome_DoesNotSeedThroughPlantedSymlink(t *testing.T) {
	stellaHome := t.TempDir()
	mustMkdirAll(t, filepath.Join(MiseToolsDir(stellaHome), "installs", "node", "20.0.0"))

	userDir := MiseUserToolsDir(stellaHome, "users", "u1")
	mustMkdirAll(t, userDir)
	// A prior session's agent planted a symlink where "installs" belongs, aimed at
	// an outside dir it wants the (unsandboxed) seeding step to write into.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(userDir, "installs")); err != nil {
		t.Fatal(err)
	}

	if err := EnsureUserMiseHome(stellaHome, userDir); err != nil {
		t.Fatalf("EnsureUserMiseHome: %v", err)
	}

	// The planted symlink is gone — "installs" is a real dir now.
	if fi, err := os.Lstat(filepath.Join(userDir, "installs")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("installs must be a real dir after seeding, lstat=%v err=%v", fi, err)
	}
	// Seeding did not escape through the link into the outside dir.
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("seeding escaped the per-user tree into the symlink target: %v", entries)
	}
	// Seeding still landed in the real tree.
	if _, err := os.Lstat(filepath.Join(userDir, "installs", "node", "20.0.0")); err != nil {
		t.Fatalf("system node@20 should be seeded into the real tree: %v", err)
	}
}

func TestEnsureUserMiseHome_RelinksPerUserShimsToRelative(t *testing.T) {
	stellaHome := t.TempDir()
	mustMkdirAll(t, filepath.Join(stellaHome, "bin"))
	mustWriteFile(t, filepath.Join(stellaHome, "bin", "mise"), "binary")

	userDir := MiseUserToolsDir(stellaHome, "users", "u1")
	mustMkdirAll(t, filepath.Join(userDir, "shims"))
	// A shim mise wrote inside a different backend's sandbox: an absolute,
	// backend-specific target that wouldn't resolve under another backend.
	shim := filepath.Join(userDir, "shims", "node")
	if err := os.Symlink("/home/stella/.stella/bin/mise", shim); err != nil {
		t.Fatal(err)
	}

	if err := EnsureUserMiseHome(stellaHome, userDir); err != nil {
		t.Fatalf("EnsureUserMiseHome: %v", err)
	}

	target, err := os.Readlink(shim)
	if err != nil {
		t.Fatalf("shim should remain a symlink: %v", err)
	}
	if filepath.IsAbs(target) {
		t.Fatalf("per-user shim must be relinked to a relative target, got %q", target)
	}
	// The relative target resolves to the local mise binary.
	if _, err := os.Stat(shim); err != nil {
		t.Fatalf("relinked shim does not resolve to bin/mise: %v", err)
	}
}

func TestEnsureUserMiseHome_PrunesDanglingSeedLinks(t *testing.T) {
	stellaHome := t.TempDir()
	userDir := MiseUserToolsDir(stellaHome, "users", "u1")
	mustMkdirAll(t, filepath.Join(userDir, "installs", "node"))
	// A seed link whose system target was pruned/upgraded away.
	dangling := filepath.Join(userDir, "installs", "node", "18.0.0")
	if err := os.Symlink("/no/such/system/install/node/18.0.0", dangling); err != nil {
		t.Fatal(err)
	}
	// A real user install alongside it must survive.
	realDir := filepath.Join(userDir, "installs", "node", "22.0.0")
	mustMkdirAll(t, realDir)

	if err := EnsureUserMiseHome(stellaHome, userDir); err != nil {
		t.Fatalf("EnsureUserMiseHome: %v", err)
	}

	if _, err := os.Lstat(dangling); !os.IsNotExist(err) {
		t.Fatalf("dangling seed link should be pruned, lstat err=%v", err)
	}
	if _, err := os.Stat(realDir); err != nil {
		t.Fatalf("real user install must survive prune: %v", err)
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
