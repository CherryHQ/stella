package binaries

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestToolNameForEntry(t *testing.T) {
	cases := []struct {
		entry    string
		wantName string
		wantOK   bool
	}{
		{"mise.gz", "mise", true},
		{"mise.exe.gz", "mise.exe", true}, // windows: extracted, not filtered out
		{"gh.gz", "", false},              // non-infra tools resolve via shims
		{"mise", "", false},               // uncompressed entries are ignored
	}
	for _, c := range cases {
		name, ok := toolNameForEntry(c.entry)
		if name != c.wantName || ok != c.wantOK {
			t.Errorf("toolNameForEntry(%q) = (%q, %v), want (%q, %v)",
				c.entry, name, ok, c.wantName, c.wantOK)
		}
	}
}

func TestEmbeddedXbergRuns(t *testing.T) {
	if !slices.Contains(ToolNames(), "xberg") {
		t.Skip("Xberg is not bundled for this platform")
	}
	home := t.TempDir()
	if err := EnsureTools(home); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ToolPath(home, "xberg"), "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run embedded Xberg: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "xberg "+xbergVersion {
		t.Fatalf("Xberg version = %q, want %q", got, "xberg "+xbergVersion)
	}
}

func TestExtractTools(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "bin")

	names := ToolNames()
	t.Logf("embedded tools: %v", names)
	if len(names) == 0 {
		t.Skip("no embedded tools (run mise run deps:sync first)")
	}

	if err := extractTools(dest); err != nil {
		t.Fatal(err)
	}

	for _, name := range names {
		path := filepath.Join(dest, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("expected %s to be non-empty", name)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("expected %s to be executable", name)
		}
		t.Logf("  %s: %d bytes", name, info.Size())
	}
}

func TestEnsureToolsIdempotent(t *testing.T) {
	ensureMu.Lock()
	ensureStates = make(map[string]*ensureState)
	ensureMu.Unlock()
	dest := t.TempDir()

	if err := EnsureTools(dest); err != nil {
		t.Fatal(err)
	}
	// Second call should be a no-op for the same destination.
	if err := EnsureTools(dest); err != nil {
		t.Fatal(err)
	}
}
