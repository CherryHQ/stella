package binaries

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	command := exec.Command(ToolPath(home, "xberg"), "--version")
	if loader := os.Getenv("STELLA_SYSTEM_TEST_XBERG_LOADER"); loader != "" {
		runtimeDir := filepath.Join(home, "bin", "xberg-v"+xbergVersion)
		libraryPath := os.Getenv("STELLA_SYSTEM_TEST_XBERG_LIBRARY_PATH") + ":" + runtimeDir
		command = exec.Command(loader, "--library-path", libraryPath, filepath.Join(runtimeDir, "xberg"), "--version")
	}
	out, err := command.CombinedOutput()
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

	shellInfo, err := os.Stat(filepath.Join(dest, shellEnvFilename))
	if err != nil {
		t.Fatalf("managed shell environment missing: %v", err)
	}
	if got := shellInfo.Mode().Perm(); got != 0o644 {
		t.Fatalf("managed shell environment mode = %o, want 644", got)
	}
	// The shell file is refreshed independently of the embedded binary
	// fingerprint so shell-only upgrades cannot leave stale startup behavior.
	shellPath := filepath.Join(dest, shellEnvFilename)
	if err := os.WriteFile(shellPath, []byte("stale\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shellPath, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := extractTools(dest); err != nil {
		t.Fatal(err)
	}
	refreshed, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(refreshed) != string(shellEnv) {
		t.Fatal("managed shell environment was not refreshed")
	}
	refreshedInfo, err := os.Stat(shellPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := refreshedInfo.Mode().Perm(); got != 0o644 {
		t.Fatalf("refreshed shell environment mode = %o, want 644", got)
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

func TestShellEnvRestoresManagedPathAfterLoginProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash login shell test is POSIX-only")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}

	stellaHome := t.TempDir()
	userTools := filepath.Join(stellaHome, "users", "u1", ".mise-tools")
	toolCacheBin := filepath.Join(stellaHome, "tool-cache", "bin")
	for _, dir := range []string{
		filepath.Join(stellaHome, "bin"),
		filepath.Join(stellaHome, ".mise-tools", "shims"),
		filepath.Join(userTools, "shims"),
		toolCacheBin,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(stellaHome, "bin", "mise"):                "#!/bin/sh\n",
		filepath.Join(stellaHome, ".mise-tools", "shims", "rg"): "#!/bin/sh\n",
		filepath.Join(userTools, "shims", "personal-tool"):      "#!/bin/sh\n",
		filepath.Join(toolCacheBin, "cached-tool"):              "#!/bin/sh\n",
		filepath.Join(stellaHome, "bin", shellEnvFilename):      string(shellEnv),
	} {
		mode := os.FileMode(0o755)
		if filepath.Base(path) == shellEnvFilename {
			mode = 0o644
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}

	wantPath := strings.Join([]string{
		filepath.Join(userTools, "shims"),
		toolCacheBin,
		filepath.Join(stellaHome, "bin"),
		filepath.Join(stellaHome, ".mise-tools", "shims"),
		"/usr/bin",
		"/bin",
	}, string(os.PathListSeparator))
	cmd := exec.Command(bash, "-lc", `printf '%s\n' "$PATH"; command -v mise; command -v personal-tool; command -v cached-tool; command -v rg`)
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		// macOS path_helper preserves inherited entries but moves them behind
		// system paths. The runner snapshot must restore the exact order.
		"PATH=" + wantPath,
		"STELLA_HOME=" + stellaHome,
		"MISE_DATA_DIR=" + userTools,
		"STELLA_RUNNER_PATH=" + wantPath,
		"BASH_ENV=" + filepath.Join(stellaHome, "bin", shellEnvFilename),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nested login bash: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 5 {
		t.Fatalf("nested login bash output = %q", out)
	}
	if lines[0] != wantPath {
		t.Fatalf("PATH = %q, want runner snapshot %q", lines[0], wantPath)
	}
	managedDirs := []string{
		filepath.Join(stellaHome, "bin"),
		filepath.Join(userTools, "shims"),
		toolCacheBin,
		filepath.Join(stellaHome, ".mise-tools", "shims"),
	}
	for i, want := range []string{
		filepath.Join(stellaHome, "bin", "mise"),
		filepath.Join(userTools, "shims", "personal-tool"),
		filepath.Join(toolCacheBin, "cached-tool"),
		filepath.Join(stellaHome, ".mise-tools", "shims", "rg"),
	} {
		if lines[i+1] != want {
			t.Fatalf("resolved command %d = %q, want %q", i, lines[i+1], want)
		}
		if strings.Count(lines[0], filepath.Dir(want)) != 1 {
			t.Fatalf("managed PATH entry %q duplicated in %q", filepath.Dir(want), lines[0])
		}
	}

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX sh is unavailable")
	}
	posix := exec.Command(sh, "-c", `. "$BASH_ENV"; . "$BASH_ENV"; printf '%s\n' "$PATH"`)
	posix.Env = cmd.Env
	posixOut, err := posix.CombinedOutput()
	if err != nil {
		t.Fatalf("source managed environment with POSIX sh: %v\n%s", err, posixOut)
	}
	posixPath := strings.TrimSpace(string(posixOut))
	if posixPath != wantPath {
		t.Fatalf("POSIX PATH = %q, want runner snapshot %q", posixPath, wantPath)
	}
	for _, dir := range managedDirs {
		if strings.Count(posixPath, dir) != 1 {
			t.Fatalf("POSIX managed PATH entry %q not idempotent in %q", dir, posixPath)
		}
	}

	customPath := filepath.Join(stellaHome, "custom", "bin") + string(os.PathListSeparator) + wantPath
	nonLogin := exec.Command(bash, "-c", `printf '%s\n' "$PATH"`)
	nonLogin.Env = slices.Clone(cmd.Env)
	for i, entry := range nonLogin.Env {
		if strings.HasPrefix(entry, "PATH=") {
			nonLogin.Env[i] = "PATH=" + customPath
			break
		}
	}
	nonLoginOut, err := nonLogin.CombinedOutput()
	if err != nil {
		t.Fatalf("nested non-login bash: %v\n%s", err, nonLoginOut)
	}
	if got := strings.TrimSpace(string(nonLoginOut)); got != customPath {
		t.Fatalf("non-login Bash PATH = %q, want parent override %q", got, customPath)
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
