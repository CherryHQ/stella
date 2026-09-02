package db

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRuntimeDir(t *testing.T, stellaHome, name string, fileSizes ...int) string {
	t.Helper()
	dir := filepath.Join(stellaHome, "pg-runtime", name, "downloaded", "trixie", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	for i, size := range fileSizes {
		path := filepath.Join(dir, "file"+string(rune('a'+i)))
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return filepath.Join(stellaHome, "pg-runtime", name)
}

// Nothing downloaded yet is the normal state on a host using an external
// database, and asking to prune there is not an error.
func TestInstalledRuntimesWithoutTheDirectory(t *testing.T) {
	installed, err := InstalledRuntimes(t.TempDir())
	if err != nil {
		t.Fatalf("InstalledRuntimes: %v", err)
	}
	if len(installed) != 0 {
		t.Fatalf("installed = %v, want none", installed)
	}
}

// The whole point of the listing is telling the runtime in use apart from the
// siblings an upgrade left behind, so that flag has to be right.
func TestInstalledRuntimesMarksTheCurrentOne(t *testing.T) {
	home := t.TempDir()
	writeRuntimeDir(t, home, "pg17.0-old-linux-amd64", 100, 200)
	writeRuntimeDir(t, home, CurrentRuntimeDir(), 50)

	installed, err := InstalledRuntimes(home)
	if err != nil {
		t.Fatalf("InstalledRuntimes: %v", err)
	}
	if len(installed) != 2 {
		t.Fatalf("installed = %v, want 2 entries", installed)
	}

	byName := map[string]InstalledRuntime{}
	for _, rt := range installed {
		byName[rt.Name] = rt
	}
	current, ok := byName[CurrentRuntimeDir()]
	if !ok || !current.Current {
		t.Errorf("%s should be marked current, got %+v", CurrentRuntimeDir(), current)
	}
	if current.Bytes != 50 {
		t.Errorf("current bytes = %d, want 50", current.Bytes)
	}
	old := byName["pg17.0-old-linux-amd64"]
	if old.Current {
		t.Error("an older version should not be marked current")
	}
	if old.Bytes != 300 {
		t.Errorf("old bytes = %d, want 300", old.Bytes)
	}
}

// RuntimeRoot and the listing must agree on the layout, or prune would offer to
// delete the directory the server is about to start from.
func TestRuntimeRootLivesUnderTheCurrentRuntimeDir(t *testing.T) {
	home := t.TempDir()
	root := RuntimeRoot(home, "trixie")
	want := filepath.Join(home, "pg-runtime", CurrentRuntimeDir(), "downloaded", "trixie")
	if root != want {
		t.Fatalf("RuntimeRoot = %s, want %s", root, want)
	}
}

// A stray file next to the version directories is not a runtime and must not
// show up as something to remove.
func TestInstalledRuntimesIgnoresLooseFiles(t *testing.T) {
	home := t.TempDir()
	writeRuntimeDir(t, home, "pg17.0-old-linux-amd64", 10)
	stray := filepath.Join(home, "pg-runtime", "README")
	if err := os.WriteFile(stray, []byte("not a runtime"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	installed, err := InstalledRuntimes(home)
	if err != nil {
		t.Fatalf("InstalledRuntimes: %v", err)
	}
	if len(installed) != 1 || installed[0].Name != "pg17.0-old-linux-amd64" {
		t.Fatalf("installed = %v, want only the version directory", installed)
	}
}
