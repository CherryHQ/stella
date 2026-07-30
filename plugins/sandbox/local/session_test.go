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
			WorkspaceRoot: root,
			WorkingDir:    root,
			Mounts:        []sandboxpkg.Mount{{HostPath: mountDir, SandboxPath: mountDir, Access: sandboxpkg.MountReadOnly}},
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
			WorkspaceRoot: root,
			WorkingDir:    root,
			Mounts:        []sandboxpkg.Mount{{HostPath: mountDir, SandboxPath: mountDir, Access: sandboxpkg.MountReadOnly}},
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

// TestSystemTree_readableButNotWritable verifies that on an isolating backend
// (sandbox STELLA_HOME differs from host), the read-only system install tree
// addressed as /opt/stella is resolvable for reads and rejected for writes.
func TestSystemTree_readableButNotWritable(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	hostSH := t.TempDir()
	hostSH, _ = filepath.EvalSymlinks(hostSH)

	skillDir := filepath.Join(hostSH, ".agents", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "refs.md"), []byte("# refs"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &localSession{
		id:                "test",
		policy:            sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root}},
		realRoot:          root,
		sandboxRoot:       root,
		stellaHomeHost:    hostSH,
		stellaHomeSandbox: "/opt/stella",
		done:              make(chan struct{}),
	}

	// The agent addresses the system tree via its sandbox view (/opt/stella/...).
	sandboxPath := "/opt/stella/.agents/skills/demo/refs.md"
	real, _, err := s.resolvePath(sandboxPath)
	if err != nil {
		t.Fatalf("resolvePath rejected system-tree read: %v", err)
	}
	if want := filepath.Join(skillDir, "refs.md"); real != want {
		t.Errorf("resolvePath real = %q, want %q", real, want)
	}

	// Writes into the system tree must be rejected.
	if _, err := s.ResolveWritePath(sandboxPath); err == nil {
		t.Fatal("expected ResolveWritePath to reject system-tree path, got nil")
	}

	// Only the RO-mounted subtrees are reachable: sibling host trees nested under
	// STELLA_HOME (users/, agents/) must stay invisible even via /opt/stella.
	if err := os.MkdirAll(filepath.Join(hostSH, "users", "u1"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, _, err := s.resolvePath("/opt/stella/users/u1/secret"); err == nil {
		t.Fatal("expected resolvePath to reject /opt/stella/users (not a mounted subtree), got nil")
	}
}

// TestAgentSkills_readableButNotWritable verifies that the agent-bound
// (system_agent) skills dir, mounted read-only at /opt/stella/agent-skills on an
// isolating backend, is resolvable for reads and rejected for writes.
func TestAgentSkills_readableButNotWritable(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	hostSH := t.TempDir()
	hostSH, _ = filepath.EvalSymlinks(hostSH)

	// AgentRoot/.agents/skills lives under STELLA_HOME (agents/<id>), distinct
	// from the system skills dir at STELLA_HOME/.agents/skills.
	agentSkills := filepath.Join(hostSH, "agents", "a1", ".agents", "skills")
	if err := os.MkdirAll(filepath.Join(agentSkills, "demo"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentSkills, "demo", "SKILL.md"), []byte("# skill"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &localSession{
		id:                "test",
		policy:            sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root, Mounts: []sandboxpkg.Mount{{HostPath: root, SandboxPath: root, Access: sandboxpkg.MountReadWrite}, {HostPath: agentSkills, SandboxPath: sandboxpkg.MountAgentSkills, Access: sandboxpkg.MountReadOnly}}}},
		realRoot:          root,
		sandboxRoot:       root,
		stellaHomeHost:    hostSH,
		stellaHomeSandbox: "/opt/stella",
		done:              make(chan struct{}),
	}

	sandboxPath := sandboxpkg.MountAgentSkills + "/demo/SKILL.md"
	real, _, err := s.resolvePath(sandboxPath)
	if err != nil {
		t.Fatalf("resolvePath rejected agent-skills read: %v", err)
	}
	if want := filepath.Join(agentSkills, "demo", "SKILL.md"); real != want {
		t.Errorf("resolvePath real = %q, want %q", real, want)
	}
	if got := s.toSandboxPath(filepath.Join(agentSkills, "demo")); got != sandboxpkg.MountAgentSkills+"/demo" {
		t.Errorf("toSandboxPath = %q, want %q", got, sandboxpkg.MountAgentSkills+"/demo")
	}
	if _, err := s.ResolveWritePath(sandboxPath); err == nil {
		t.Fatal("expected ResolveWritePath to reject agent-skills path, got nil")
	}
}

// TestSystemDBSkills_readableButNotWritable verifies that the DB-installed
// system skills dir, mounted read-only at /opt/stella/db-skills on an isolating
// backend, is resolvable for reads and rejected for writes — and stays distinct
// from the shipped built-in system skills dir under STELLA_HOME.
func TestSystemDBSkills_readableButNotWritable(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	hostSH := t.TempDir()
	hostSH, _ = filepath.EvalSymlinks(hostSH)

	dbSkills := filepath.Join(hostSH, ".agents", "db-skills")
	if err := os.MkdirAll(filepath.Join(dbSkills, "demo"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbSkills, "demo", "SKILL.md"), []byte("# skill"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := &localSession{
		id:                "test",
		policy:            sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root, Mounts: []sandboxpkg.Mount{{HostPath: root, SandboxPath: root, Access: sandboxpkg.MountReadWrite}, {HostPath: dbSkills, SandboxPath: sandboxpkg.MountSystemDBSkills, Access: sandboxpkg.MountReadOnly}}}},
		realRoot:          root,
		sandboxRoot:       root,
		stellaHomeHost:    hostSH,
		stellaHomeSandbox: "/opt/stella",
		done:              make(chan struct{}),
	}

	sandboxPath := sandboxpkg.MountSystemDBSkills + "/demo/SKILL.md"
	real, _, err := s.resolvePath(sandboxPath)
	if err != nil {
		t.Fatalf("resolvePath rejected system-db-skills read: %v", err)
	}
	if want := filepath.Join(dbSkills, "demo", "SKILL.md"); real != want {
		t.Errorf("resolvePath real = %q, want %q", real, want)
	}
	if got := s.toSandboxPath(filepath.Join(dbSkills, "demo")); got != sandboxpkg.MountSystemDBSkills+"/demo" {
		t.Errorf("toSandboxPath = %q, want %q", got, sandboxpkg.MountSystemDBSkills+"/demo")
	}
	if _, err := s.ResolveWritePath(sandboxPath); err == nil {
		t.Fatal("expected ResolveWritePath to reject system-db-skills path, got nil")
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
			WorkspaceRoot: root,
			WorkingDir:    root,
			Mounts:        []sandboxpkg.Mount{{HostPath: mountDir, SandboxPath: mountDir, Access: sandboxpkg.MountReadOnly}},
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
	sandboxRoot, realRoot := resolveSandboxRoot(policy)
	adjusted := f.adjustPolicy(policy, sandboxRoot, realRoot, "", "")

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
	sandboxRoot, realRoot := resolveSandboxRoot(policy)
	adjusted := f.adjustPolicy(policy, sandboxRoot, realRoot, "", "")

	wantShims := sandboxSH + "/users/u1/.mise-tools/shims"
	if !strings.Contains(adjusted.Env["PATH"], wantShims) {
		t.Fatalf("PATH must include per-user shims %q, got %q", wantShims, adjusted.Env["PATH"])
	}
}

// TestAdjustPolicy_perUserMiseInStellaHomeFrame verifies that the per-user mise
// tree — under the STELLA_HOME frame ($STELLA_HOME/users/{id}/.mise-tools, a
// sibling of the user-data root) — is expressed under the sandbox STELLA_HOME
// (/opt/stella/users/{id}/.mise-tools), sharing the system tree's root so the
// relative seed/shim symlinks resolve. The system config path also stays under
// the sandbox STELLA_HOME, and the host workspace trusted entry collapses onto
// /workspace. This is the production layout (#505): the tree must NOT land in the
// /user frame, which would split it from the system tree across sandbox roots.
func TestAdjustPolicy_perUserMiseInStellaHomeFrame(t *testing.T) {
	hostSH := "/home/user/.stella"
	userHome := hostSH + "/users/u1"
	agentDir := userHome + "/agents/a1"
	userData := userHome + "/data"
	miseHome := userHome + "/.mise-tools" // sibling of data, under STELLA_HOME
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: agentDir, Mounts: []sandboxpkg.Mount{{HostPath: agentDir, SandboxPath: "/workspace", Access: sandboxpkg.MountReadWrite}, {HostPath: userData, SandboxPath: "/user", Access: sandboxpkg.MountReadWrite}}},
		Env: map[string]string{
			"MISE_DATA_DIR":             miseHome,
			"MISE_CACHE_DIR":            miseHome + "/cache",
			"MISE_GLOBAL_CONFIG_FILE":   hostSH + "/.mise-tools/configs/_builtin.toml",
			"MISE_TRUSTED_CONFIG_PATHS": hostSH + "/.mise-tools/configs/_builtin.toml:/workspace:" + agentDir,
			"LARKSUITE_CLI_CONFIG_DIR":  userData + "/.lark-cli",
			"LARKSUITE_CLI_DATA_DIR":    userData + "/.lark-cli/data",
		},
	}
	f := &Factory{cfg: Config{StellaHome: hostSH}}
	// Drive adjustPolicy with explicit remapping roots so the two-root composition
	// is exercised on every platform, not only where resolve*Root remaps (Linux).
	sandboxRoot, realRoot := "/workspace", agentDir
	userDataSandbox, userDataReal := "/user", userData
	sandboxSH := adjustStellaHome(hostSH)
	adjusted := f.adjustPolicy(policy, sandboxRoot, realRoot, userDataSandbox, userDataReal)

	for _, tc := range []struct{ key, want string }{
		{"MISE_DATA_DIR", sandboxSH + "/users/u1/.mise-tools"},
		{"MISE_CACHE_DIR", sandboxSH + "/users/u1/.mise-tools/cache"},
		{"MISE_GLOBAL_CONFIG_FILE", sandboxSH + "/.mise-tools/configs/_builtin.toml"},
		{"MISE_TRUSTED_CONFIG_PATHS", sandboxSH + "/.mise-tools/configs/_builtin.toml:/workspace"},
		{"LARKSUITE_CLI_CONFIG_DIR", "/user/.lark-cli"},
		{"LARKSUITE_CLI_DATA_DIR", "/user/.lark-cli/data"},
	} {
		if got := adjusted.Env[tc.key]; got != tc.want {
			t.Errorf("env[%s] = %q, want %q", tc.key, got, tc.want)
		}
	}
	if wantShims := sandboxSH + "/users/u1/.mise-tools/shims"; !strings.Contains(adjusted.Env["PATH"], wantShims) {
		t.Errorf("PATH must include STELLA_HOME-frame shims %q, got %q", wantShims, adjusted.Env["PATH"])
	}
	if strings.Contains(adjusted.Env["PATH"], userData+"/") {
		t.Errorf("PATH must not leak the host user-data path %q: %q", userData, adjusted.Env["PATH"])
	}
}

// TestAdjustPolicy_homeAndXDG verifies HOME remains the agent workspace while
// every persistent XDG directory uses the shared per-principal /user root.
// STELLA_USER_DIR exposes that root too.
func TestAdjustPolicy_homeAndXDG(t *testing.T) {
	root := t.TempDir()
	userData := filepath.Join(root, "data")
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    filepath.Join(root, "projects", "p"),
			Mounts:        []sandboxpkg.Mount{{HostPath: root, SandboxPath: sandboxpkg.MountWorkspace, Access: sandboxpkg.MountReadWrite}, {HostPath: userData, SandboxPath: sandboxpkg.MountUserData, Access: sandboxpkg.MountReadWrite}},
		},
	}
	f := &Factory{cfg: Config{StellaHome: t.TempDir()}}
	sandboxRoot, realRoot := resolveSandboxRoot(policy)
	userDataSandbox, userDataReal := resolveUserDataRoot(policy)
	env := f.adjustPolicy(policy, sandboxRoot, realRoot, userDataSandbox, userDataReal).Env

	for _, tc := range []struct{ key, want string }{
		{"HOME", sandboxRoot},
		{"STELLA_USER_DIR", userDataSandbox},
		{"XDG_CACHE_HOME", filepath.Join(userDataSandbox, ".cache")},
		{"XDG_CONFIG_HOME", filepath.Join(userDataSandbox, ".config")},
		{"XDG_DATA_HOME", filepath.Join(userDataSandbox, ".local", "share")},
		{"XDG_STATE_HOME", filepath.Join(userDataSandbox, ".local", "state")},
	} {
		if env[tc.key] != tc.want {
			t.Errorf("env[%s] = %q, want %q", tc.key, env[tc.key], tc.want)
		}
	}
	if _, ok := env["XDG_RUNTIME_DIR"]; ok {
		t.Error("XDG_RUNTIME_DIR must not be set")
	}
}

// TestAdjustPolicy_noUserDataFallsBackToWorkspace verifies a user-less session
// (no shared user-data root) keeps every XDG directory under HOME and sets no
// STELLA_USER_DIR, so nothing is shared with other agents.
func TestAdjustPolicy_noUserDataFallsBackToWorkspace(t *testing.T) {
	root := t.TempDir()
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root},
	}
	f := &Factory{cfg: Config{StellaHome: t.TempDir()}}
	sandboxRoot, realRoot := resolveSandboxRoot(policy)
	userDataSandbox, userDataReal := resolveUserDataRoot(policy)
	env := f.adjustPolicy(policy, sandboxRoot, realRoot, userDataSandbox, userDataReal).Env

	for _, tc := range []struct{ key, want string }{
		{"XDG_CACHE_HOME", filepath.Join(sandboxRoot, ".cache")},
		{"XDG_CONFIG_HOME", filepath.Join(sandboxRoot, ".config")},
		{"XDG_DATA_HOME", filepath.Join(sandboxRoot, ".local", "share")},
		{"XDG_STATE_HOME", filepath.Join(sandboxRoot, ".local", "state")},
	} {
		if env[tc.key] != tc.want {
			t.Errorf("env[%s] = %q, want %q", tc.key, env[tc.key], tc.want)
		}
	}
	if v, ok := env["STELLA_USER_DIR"]; ok {
		t.Errorf("STELLA_USER_DIR should be unset for a user-less session, got %q", v)
	}
}

// TestResolvePath_twoRoots verifies the host-side path resolver recognizes both
// top-level roots: /workspace maps to the agent dir and /user to the shared
// user-data dir, while an escape to a sibling and a symlink component under /user
// are rejected. Without the /user arm the file tools would refuse /user even
// though bash inside the sandbox can reach it (the critical gap this guards).
func TestResolvePath_twoRoots(t *testing.T) {
	agentReal := t.TempDir()
	userReal := t.TempDir()
	s := &localSession{
		realRoot:        agentReal,
		sandboxRoot:     "/workspace",
		userDataReal:    userReal,
		userDataSandbox: "/user",
		policy: sandboxpkg.Policy{
			Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/workspace"},
		},
	}

	got, err := s.ResolveWritePath("/user/assets/x.txt")
	if err != nil {
		t.Fatalf("ResolveWritePath(/user/...): %v", err)
	}
	if want := filepath.Join(userReal, "assets", "x.txt"); got != want {
		t.Errorf("/user write resolved to %q, want %q", got, want)
	}

	got, err = s.ResolveWritePath("/workspace/main.go")
	if err != nil {
		t.Fatalf("ResolveWritePath(/workspace/...): %v", err)
	}
	if want := filepath.Join(agentReal, "main.go"); got != want {
		t.Errorf("/workspace write resolved to %q, want %q", got, want)
	}

	if _, err := s.ResolvePath("/workspace/../other/secret"); err == nil {
		t.Error("escape from /workspace to a sibling must be rejected")
	}

	// A symlink component under /user must be rejected (no traversal escape).
	if err := os.Symlink(userReal, filepath.Join(userReal, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolvePath("/user/loop/x"); err == nil {
		t.Error("symlink component under /user must be rejected")
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
