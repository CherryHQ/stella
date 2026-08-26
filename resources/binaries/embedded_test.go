package binaries

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestPlatformRuntimesMatchEmbeddedAssets pins the table against what is really
// embedded: every runtime this platform claims must have a readable archive with
// a version stamp, since extraction now takes both from the artifact itself.
func TestPlatformRuntimesMatchEmbeddedAssets(t *testing.T) {
	runtimes := platformRuntimes()
	if len(runtimes) == 0 {
		t.Fatal("no runtimes embedded; embed_*.go names archives exactly, so this cannot compile without them")
	}
	seen := map[string]bool{}
	for _, rt := range runtimes {
		if seen[rt.name] {
			t.Errorf("duplicate runtime name %q", rt.name)
		}
		seen[rt.name] = true
		version, err := archiveVersion(rt.archive)
		if err != nil {
			t.Errorf("%s: %v", rt.name, err)
			continue
		}
		if version == "" {
			t.Errorf("%s: archive %s carries no version stamp", rt.name, rt.archive)
		}
	}
	if !seen["mise"] && !seen["mise.exe"] {
		t.Error("mise must be embedded on every supported platform")
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
	// Stdout only: Xberg's bundled onnxruntime writes an unrelated CPU-vendor
	// warning to stderr on hosts it cannot identify (VMs, some ARM servers).
	// Callers parse stdout, so that is what this asserts.
	cmd := exec.Command(ToolPath(home, "xberg"), "--version")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run embedded Xberg: %v: %s", err, stderr.String())
	}
	version, err := archiveVersion("xberg.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "xberg "+version {
		t.Fatalf("Xberg version = %q, want %q", got, "xberg "+version)
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

// TestExtractedToolsShareOnePermissionContract pins the invariant that broke
// Xberg: a bundle installed by one UID must stay usable by another, exactly
// like the single-file mise binary next to it.
func TestExtractedToolsShareOnePermissionContract(t *testing.T) {
	dest := t.TempDir()
	if err := extractTools(dest); err != nil {
		t.Fatal(err)
	}

	err := filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dest {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // the launcher's own mode is irrelevant; its target's is not
		}
		perm := info.Mode().Perm()
		if d.IsDir() {
			if perm != toolDirMode {
				t.Errorf("%s: dir mode = %o, want %o", path, perm, toolDirMode)
			}
			return nil
		}
		// Group and other must at least be able to read what bin/ exposes.
		if perm&0o044 != 0o044 {
			t.Errorf("%s: file mode = %o, not readable by group/other", path, perm)
		}
		if perm&0o100 != 0 && perm&0o011 != 0o011 {
			t.Errorf("%s: file mode = %o, owner-executable but not group/other", path, perm)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestExtractToolsRepairsPrivateXbergBundle covers the upgrade path: the archive
// fingerprint still matches, so extraction is skipped entirely and only the
// explicit repair can widen a directory left at 0700 by an older Stella.
func TestExtractToolsRepairsPrivateXbergBundle(t *testing.T) {
	if !slices.Contains(ToolNames(), "xberg") {
		t.Skip("no embedded Xberg for this platform")
	}
	dest := t.TempDir()
	if err := extractTools(dest); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(dest, "xberg-v"+mustXbergVersion(t))
	misePath := filepath.Join(dest, "mise")
	for _, path := range []string{runtimeDir, misePath, filepath.Join(runtimeDir, "xberg")} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := extractTools(dest); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]os.FileMode{
		runtimeDir:                         toolDirMode,
		misePath:                           toolExecMode,
		filepath.Join(runtimeDir, "xberg"): toolExecMode,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: mode = %o, want %o", path, got, want)
		}
	}
}

// TestVerifyToolsRejectsUnusableBundle proves the contract is enforced, not just
// documented: a runtime that exists but only its owner can reach must fail
// verification rather than pass as "installed".
func TestVerifyToolsRejectsUnusableBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX mode bits")
	}
	if !slices.Contains(ToolNames(), "xberg") {
		t.Skip("no embedded Xberg for this platform")
	}
	home := t.TempDir()
	if err := extractTools(BinDir(home)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTools(home); err != nil {
		t.Fatalf("freshly extracted tools must verify: %v", err)
	}

	runtimeDir := filepath.Join(BinDir(home), "xberg-v"+mustXbergVersion(t))
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTools(home); err == nil {
		t.Fatal("VerifyTools accepted an owner-only bundle directory")
	}
}

func mustXbergVersion(t *testing.T) string {
	t.Helper()
	version, err := archiveVersion("xberg.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// TestExtractXbergBundleRemovesSupersededVersions covers the upgrade path that
// used to leak ~140 MB per version: the launcher moves to the new bundle, and
// every directory it no longer points at must go.
func TestExtractXbergBundleRemovesSupersededVersions(t *testing.T) {
	if !slices.Contains(ToolNames(), "xberg") {
		t.Skip("no embedded Xberg for this platform")
	}
	dest := t.TempDir()
	stale := filepath.Join(dest, "xberg-v0.0.1")
	if err := os.MkdirAll(stale, toolDirMode); err != nil {
		t.Fatal(err)
	}

	version := mustXbergVersion(t)
	if err := extractXbergBundle(toolsDir+"/xberg.tar.gz", dest, version); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("superseded bundle %s survived extraction (err=%v)", stale, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "xberg-v"+version, "xberg")); err != nil {
		t.Errorf("current bundle missing: %v", err)
	}
}

// TestExtractSingleFilePublishesAtomically guards against rewriting a live
// binary in place, which risks ETXTBSY while a sandbox shell is executing it.
func TestExtractSingleFilePublishesAtomically(t *testing.T) {
	dest := t.TempDir()
	archive := "mise.gz"
	if runtime.GOOS == "windows" {
		archive = "mise.exe.gz"
	}
	if err := extractSingleFile(toolsDir+"/"+archive, dest, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tool-") {
			t.Errorf("staging file %s left behind", entry.Name())
		}
	}
}
