package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestFactory_basics(t *testing.T) {
	f := NewFactory()
	if f.(*Factory).Name() != "local" {
		t.Error("expected name 'local'")
	}
	if !f.(*Factory).Available() {
		t.Error("expected Available to return true")
	}
	skipIfBwrapNotFunctional(t)
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: t.TempDir()},
	}
	if err := f.(*Factory).Supported(policy); err != nil {
		t.Errorf("Supported: unexpected error: %v", err)
	}
}

func TestFactory_createSession(t *testing.T) {
	skipIfBwrapNotFunctional(t)
	root := t.TempDir()
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
		InheritEnv: true,
	}
	f := NewFactory()
	sess, err := f.(*Factory).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.(*localSession).Close() //nolint:errcheck

	if sess.(*localSession).WorkspaceRoot() == "" {
		t.Error("expected non-empty WorkspaceRoot")
	}
	if !sess.(*localSession).Alive() {
		t.Error("expected Alive=true before close")
	}
}

func TestLocalSession_closeAndAlive(t *testing.T) {
	s, _ := newTestSession(t)
	if !s.Alive() {
		t.Fatal("expected Alive=true initially")
	}
	_ = s.Close()
	if s.Alive() {
		t.Error("expected Alive=false after close")
	}
	// second close must be a no-op
	if err := s.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

func TestLocalSession_doneChanClosed(t *testing.T) {
	s, _ := newTestSession(t)
	done := s.Done()
	_ = s.Close()
	select {
	case <-done:
	default:
		t.Error("done channel should be closed after Close()")
	}
}

func TestLocalSession_workspaceAndWorkingDir(t *testing.T) {
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: resolved,
			WorkingDir:    resolved,
		},
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		realRoot:    resolved,
		sandboxRoot: resolved,
		done:        make(chan struct{}),
	}
	if s.WorkspaceRoot() != resolved {
		t.Errorf("WorkspaceRoot = %q, want %q", s.WorkspaceRoot(), resolved)
	}
	if s.WorkingDir() != resolved {
		t.Errorf("WorkingDir = %q, want %q", s.WorkingDir(), resolved)
	}
	if s.Policy().Filesystem.WorkspaceRoot != resolved {
		t.Error("Policy not preserved")
	}
}

// newTestSession returns a localSession with a temporary workspace root.
// The root is resolved through EvalSymlinks so that macOS /var → /private/var
// symlinks do not cause false path-escape rejections.
// sandboxRoot and realRoot are both set to root (no remapping in tests).
func newTestSession(t *testing.T) (*localSession, string) {
	t.Helper()
	rawRoot := t.TempDir()
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", rawRoot, err)
	}
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
		InheritEnv: true,
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		realRoot:    root,
		sandboxRoot: root,
		done:        make(chan struct{}),
	}
	return s, root
}

// TestResolvePath_rejectsOutsideRoot verifies that ResolvePath returns an error
// when the resolved path is outside the workspace root.
func TestResolvePath_rejectsOutsideRoot(t *testing.T) {
	s, root := newTestSession(t)

	// A path that traverses above the root.
	outside := filepath.Join(root, "..", "escape")
	_, err := s.ResolvePath(outside)
	if err == nil {
		t.Fatalf("expected error for path outside workspace root, got nil")
	}
}

// TestResolvePath_acceptsInsideRoot verifies that ResolvePath accepts paths
// that are within the workspace root.
func TestResolvePath_acceptsInsideRoot(t *testing.T) {
	s, root := newTestSession(t)

	// Create a file inside the root.
	f := filepath.Join(root, "file.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := s.ResolvePath(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The resolved path should equal f (root is already EvalSymlinks-resolved).
	if got != f {
		t.Errorf("expected %q, got %q", f, got)
	}
}

// TestResolvePath_realRootSymlink verifies that resolvePath works when realRoot
// contains symlink components (e.g. /home/user → /Users/user on macOS autofs,
// or any symlinked home directory). CreateSession resolves realRoot through
// symlinks so the pathWithinRoot comparison succeeds.
func TestResolvePath_realRootSymlink(t *testing.T) {
	// Create the actual workspace directory.
	actualDir := t.TempDir()
	actualDir, _ = filepath.EvalSymlinks(actualDir)
	if err := os.WriteFile(filepath.Join(actualDir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create a symlink that points to the actual directory.
	symlinkParent := t.TempDir()
	symlinkPath := filepath.Join(symlinkParent, "link")
	if err := os.Symlink(actualDir, symlinkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// Simulate CreateSession's EvalSymlinks on realRoot: the session stores
	// the resolved root, not the symlinked one.
	resolvedRoot := actualDir // as if EvalSymlinks(symlinkPath) ran

	s := &localSession{
		id:          "test",
		policy:      sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: resolvedRoot, WorkingDir: resolvedRoot}},
		realRoot:    resolvedRoot,
		sandboxRoot: "/workspace",
		done:        make(chan struct{}),
	}

	got, err := s.ResolvePath("/workspace/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(actualDir, "main.go")
	if got != want {
		t.Errorf("ResolvePath = %q, want %q", got, want)
	}
}

// TestToRealPath verifies the sandbox→real path translation.
func TestToRealPath(t *testing.T) {
	s := &localSession{
		sandboxRoot: "/workspace",
		realRoot:    "/home/stella/.stella-dev/workspaces/1",
	}

	tests := []struct {
		in   string
		want string
	}{
		{"/workspace/foo.go", "/home/stella/.stella-dev/workspaces/1/foo.go"},
		{"/workspace/sub/dir/file.go", "/home/stella/.stella-dev/workspaces/1/sub/dir/file.go"},
		// Exact root
		{"/workspace", "/home/stella/.stella-dev/workspaces/1"},
		// Outside sandboxRoot — returned unchanged
		{"/etc/passwd", "/etc/passwd"},
	}
	for _, tc := range tests {
		got := s.toRealPath(tc.in)
		if got != tc.want {
			t.Errorf("toRealPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestToRealPath_noRemap verifies that toRealPath is a no-op when sandboxRoot == realRoot.
func TestToRealPath_noRemap(t *testing.T) {
	root := "/tmp/ws"
	s := &localSession{sandboxRoot: root, realRoot: root}
	input := "/tmp/ws/foo.go"
	got := s.toRealPath(input)
	if got != input {
		t.Errorf("toRealPath(%q) = %q, want unchanged %q", input, got, input)
	}
}

// TestResolvePath_remapped verifies that ResolvePath translates a sandbox-space
// path to the real host path when sandboxRoot != realRoot.
func TestResolvePath_remapped(t *testing.T) {
	rawRoot := t.TempDir()
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	// Create a file in the real root.
	f := filepath.Join(root, "main.go")
	if err := os.WriteFile(f, []byte("package main"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		realRoot:    root,
		sandboxRoot: "/workspace",
		done:        make(chan struct{}),
	}

	// Agent passes sandbox-space path.
	got, err := s.ResolvePath("/workspace/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("ResolvePath(/workspace/main.go) = %q, want %q", got, f)
	}
}

// TestResolvePath_extraMountAllowed verifies that ResolvePath accepts paths
// within an ExtraReadOnlyMount even when they are outside the workspace root.
func TestResolvePath_extraMountAllowed(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	mountDir := t.TempDir()
	mountDir, _ = filepath.EvalSymlinks(mountDir)

	skillFile := filepath.Join(mountDir, "skill.py")
	if err := os.WriteFile(skillFile, []byte("# skill"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot:       root,
			WorkingDir:          root,
			ExtraReadOnlyMounts: []string{mountDir},
		},
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		realRoot:    root,
		sandboxRoot: root,
		done:        make(chan struct{}),
	}

	got, err := s.ResolvePath(skillFile)
	if err != nil {
		t.Fatalf("unexpected error for extra mount path: %v", err)
	}
	if got != skillFile {
		t.Errorf("ResolvePath = %q, want %q", got, skillFile)
	}
}

// TestResolvePath_rejectsAdjacentToExtraMount verifies that a path adjacent to
// (but not within) an ExtraReadOnlyMount is still rejected.
// TestResolveWritePath_rejectsExtraMount verifies that ResolveWritePath rejects
// paths within ExtraReadOnlyMounts even though ResolvePath accepts them.
func TestResolveWritePath_rejectsExtraMount(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	mountDir := t.TempDir()
	mountDir, _ = filepath.EvalSymlinks(mountDir)

	skillFile := filepath.Join(mountDir, "skill.py")
	if err := os.WriteFile(skillFile, []byte("# skill"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot:       root,
			WorkingDir:          root,
			ExtraReadOnlyMounts: []string{mountDir},
		},
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		realRoot:    root,
		sandboxRoot: root,
		done:        make(chan struct{}),
	}

	// ResolvePath should accept it.
	if _, err := s.ResolvePath(skillFile); err != nil {
		t.Fatalf("ResolvePath unexpectedly rejected extra mount path: %v", err)
	}

	// ResolveWritePath must reject it.
	_, err := s.ResolveWritePath(skillFile)
	if err == nil {
		t.Fatal("expected ResolveWritePath to reject read-only mount path, got nil")
	}
}

// TestResolveWritePath_acceptsWorkspace verifies that ResolveWritePath allows
// paths within the writable workspace root.
func TestResolveWritePath_acceptsWorkspace(t *testing.T) {
	s, root := newTestSession(t)

	f := filepath.Join(root, "output.txt")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := s.ResolveWritePath(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("ResolveWritePath = %q, want %q", got, f)
	}
}

func TestResolvePath_rejectsAdjacentToExtraMount(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	mountDir := t.TempDir()
	mountDir, _ = filepath.EvalSymlinks(mountDir)

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot:       root,
			WorkingDir:          root,
			ExtraReadOnlyMounts: []string{mountDir},
		},
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		realRoot:    root,
		sandboxRoot: root,
		done:        make(chan struct{}),
	}

	// A sibling directory of mountDir — not inside any mount.
	adjacent := filepath.Join(filepath.Dir(mountDir), "adjacent")
	_, err := s.ResolvePath(adjacent)
	if err == nil {
		t.Fatal("expected error for path adjacent to extra mount, got nil")
	}
}

func TestResolvePath_rejectsSymlinkParentForMissingPath(t *testing.T) {
	s, root := newTestSession(t)
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := s.ResolvePath(filepath.Join(link, "new.txt"))
	if err == nil {
		t.Fatal("expected symlink parent to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got: %v", err)
	}
}

func TestResolveCwd_rejectsOutsideRoot(t *testing.T) {
	s, root := newTestSession(t)
	outside := filepath.Join(root, "..")

	_, _, err := s.resolveCwd(outside)
	if err == nil {
		t.Fatal("expected cwd outside workspace to be rejected")
	}
	if !strings.Contains(err.Error(), "outside workspace root") {
		t.Fatalf("expected outside-root error, got: %v", err)
	}
}

// TestAdjustPolicy_rewritesMiseEnvPaths verifies that adjustPolicy rewrites
// MISE_* path-valued env vars from host STELLA_HOME to the sandbox-adjusted
// path so that mise shims resolve correctly inside bwrap.
func TestAdjustPolicy_rewritesMiseEnvPaths(t *testing.T) {
	hostSH := "/home/user/.stella"
	sandboxSH := adjustStellaHome(hostSH)

	policy := sandboxpkg.Policy{
		Env: map[string]string{
			"MISE_DATA_DIR":               hostSH + "/.mise-tools",
			"MISE_CONFIG_DIR":             hostSH + "/.mise-tools/config",
			"MISE_CACHE_DIR":              hostSH + "/.mise-tools/cache",
			"MISE_STATE_DIR":              hostSH + "/.mise-tools/state",
			"MISE_GLOBAL_CONFIG_FILE":     hostSH + "/.mise-tools/configs/_builtin.toml",
			"MISE_TRUSTED_CONFIG_PATHS":   hostSH + "/.mise-tools/configs/_builtin.toml",
			"MISE_YES":                    "1",
			"MISE_NOT_FOUND_AUTO_INSTALL": "false",
			"OTHER_VAR":                   "keep-as-is",
		},
	}

	f := &Factory{cfg: Config{StellaHome: hostSH}}
	adjusted := f.adjustPolicy(policy)

	if hostSH == sandboxSH {
		t.Skip("no path remapping on this platform")
	}

	for _, tc := range []struct {
		key  string
		want string
	}{
		{"MISE_DATA_DIR", sandboxSH + "/.mise-tools"},
		{"MISE_CONFIG_DIR", sandboxSH + "/.mise-tools/config"},
		{"MISE_CACHE_DIR", sandboxSH + "/.mise-tools/cache"},
		{"MISE_STATE_DIR", sandboxSH + "/.mise-tools/state"},
		{"MISE_GLOBAL_CONFIG_FILE", sandboxSH + "/.mise-tools/configs/_builtin.toml"},
		{"MISE_TRUSTED_CONFIG_PATHS", sandboxSH + "/.mise-tools/configs/_builtin.toml"},
		{"MISE_YES", "1"},
		{"MISE_NOT_FOUND_AUTO_INSTALL", "false"},
		{"STELLA_HOME", sandboxSH},
	} {
		got := adjusted.Env[tc.key]
		if got != tc.want {
			t.Errorf("env[%s] = %q, want %q", tc.key, got, tc.want)
		}
	}

	if adjusted.Env["OTHER_VAR"] != "keep-as-is" {
		t.Errorf("OTHER_VAR was unexpectedly modified: %q", adjusted.Env["OTHER_VAR"])
	}
}

// TestAdjustPolicy_perUserMiseShimsOnPath verifies that when the runtime env
// points MISE_DATA_DIR at a per-user tree, adjustPolicy prepends that tree's
// shims (remapped into the sandbox) onto PATH. The per-user shims are derived
// from the env, not a mise-specific policy field — exercising PerUserMiseDataDir.
func TestAdjustPolicy_perUserMiseShimsOnPath(t *testing.T) {
	hostSH := "/home/user/.stella"
	sandboxSH := adjustStellaHome(hostSH)
	if hostSH == sandboxSH {
		t.Skip("no path remapping on this platform")
	}

	policy := sandboxpkg.Policy{
		Env: map[string]string{"MISE_DATA_DIR": hostSH + "/users/u1/.mise-tools"},
	}
	f := &Factory{cfg: Config{StellaHome: hostSH}}
	adjusted := f.adjustPolicy(policy)

	wantShims := sandboxSH + "/users/u1/.mise-tools/shims"
	if !strings.Contains(adjusted.Env["PATH"], wantShims) {
		t.Fatalf("PATH must include per-user shims %q, got %q", wantShims, adjusted.Env["PATH"])
	}
}

// TestBuildEnv_denyListFiltersVaultKey verifies that STELLA_VAULT_KEY is never
// copied from the host environment into the sandbox env, even when InheritEnv
// is true, while other env vars (e.g. PATH) remain present.
func TestBuildEnv_denyListFiltersVaultKey(t *testing.T) {
	t.Setenv("STELLA_VAULT_KEY", "age-secret-key-1AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: t.TempDir(),
			WorkingDir:    t.TempDir(),
		},
		InheritEnv: true,
	}

	env := buildEnv(policy, nil)

	// STELLA_VAULT_KEY must not appear in the sandbox env.
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if key == "STELLA_VAULT_KEY" {
			t.Fatalf("STELLA_VAULT_KEY must not be present in sandbox env, but got: %q", kv)
		}
	}

	// At least one other var (PATH) should be present since InheritEnv is true.
	found := false
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if key == "PATH" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected PATH to be present in sandbox env when InheritEnv is true")
	}
}

// TestExec_nonzeroExitCode verifies that Exec returns a non-zero ExitCode for
// failing commands and does not surface it as a Go error.
func TestExec_nonzeroExitCode(t *testing.T) {
	skipIfBwrapNotFunctional(t)
	s, _ := newTestSession(t)
	result, err := s.Exec(context.Background(), "exit 42", sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", result.ExitCode)
	}
}

// TestExec_success verifies that a successful command returns exit code 0 and
// captures stdout correctly.
func TestExec_success(t *testing.T) {
	skipIfBwrapNotFunctional(t)
	s, _ := newTestSession(t)
	result, err := s.Exec(context.Background(), "echo hello", sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

// TestResolvePath_tmpMountAllowed verifies that ResolvePath accepts paths within
// a tmpMount (e.g. /tmp) and translates them to the real host path.
func TestResolvePath_tmpMountAllowed(t *testing.T) {
	realTmpDir := t.TempDir()
	realTmpDir, _ = filepath.EvalSymlinks(realTmpDir)
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	f := filepath.Join(realTmpDir, "work", "out.json")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &localSession{
		id:          "test",
		policy:      sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root}},
		realRoot:    root,
		sandboxRoot: "/workspace",
		tmpMounts:   []tmpMount{{sandboxPath: "/tmp", realPath: realTmpDir}},
		done:        make(chan struct{}),
	}

	// Agent path in sandbox space.
	got, err := s.ResolvePath("/tmp/work/out.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("ResolvePath = %q, want %q", got, f)
	}
}

// TestResolvePath_varTmpMountAllowed verifies that /var/tmp paths are accepted
// when a tmpMount covers /var/tmp.
func TestResolvePath_varTmpMountAllowed(t *testing.T) {
	realVarTmp := t.TempDir()
	realVarTmp, _ = filepath.EvalSymlinks(realVarTmp)
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	f := filepath.Join(realVarTmp, "cache.bin")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &localSession{
		id:          "test",
		policy:      sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root}},
		realRoot:    root,
		sandboxRoot: "/workspace",
		tmpMounts:   []tmpMount{{sandboxPath: "/var/tmp", realPath: realVarTmp}},
		done:        make(chan struct{}),
	}

	got, err := s.ResolvePath("/var/tmp/cache.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("ResolvePath = %q, want %q", got, f)
	}
}

// TestToRealPath_tmpMapping verifies that /tmp and /var/tmp sandbox paths are
// translated to their real host paths via tmpMounts, while workspace paths
// still go through the sandboxRoot→realRoot mapping.
func TestToRealPath_tmpMapping(t *testing.T) {
	realTmp := t.TempDir()
	realVarTmp := t.TempDir()
	root := t.TempDir()

	s := &localSession{
		realRoot:    root,
		sandboxRoot: "/workspace",
		tmpMounts: []tmpMount{
			{sandboxPath: "/tmp", realPath: realTmp},
			{sandboxPath: "/var/tmp", realPath: realVarTmp},
		},
	}

	tests := []struct {
		in   string
		want string
	}{
		{"/tmp/foo.txt", filepath.Join(realTmp, "foo.txt")},
		{"/tmp", realTmp},
		{"/var/tmp/bar", filepath.Join(realVarTmp, "bar")},
		{"/workspace/src/main.go", filepath.Join(root, "src/main.go")},
	}
	for _, tc := range tests {
		got := s.toRealPath(tc.in)
		if got != tc.want {
			t.Errorf("toRealPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveWritePath_allowsTmp verifies that ResolveWritePath permits paths
// inside tmpMounts (they are writable, not read-only).
func TestResolveWritePath_allowsTmp(t *testing.T) {
	realTmpDir := t.TempDir()
	realTmpDir, _ = filepath.EvalSymlinks(realTmpDir)
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	f := filepath.Join(realTmpDir, "out.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &localSession{
		id:          "test",
		policy:      sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root}},
		realRoot:    root,
		sandboxRoot: "/workspace",
		tmpMounts:   []tmpMount{{sandboxPath: "/tmp", realPath: realTmpDir}},
		done:        make(chan struct{}),
	}

	got, err := s.ResolveWritePath("/tmp/out.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("ResolveWritePath = %q, want %q", got, f)
	}
}
