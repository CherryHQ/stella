package none

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

func layoutFor(workspace, workingDir string, mounts ...hostlayout.Mount) hostlayout.Layout {
	if len(mounts) == 0 && workspace != "" {
		mounts = []hostlayout.Mount{{Source: workspace, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite}}
	}
	return hostlayout.Layout{WorkspaceSource: workspace, WorkingDirSource: workingDir, Mounts: mounts}
}

func TestFactory_basics(t *testing.T) {
	root := t.TempDir()
	f := NewFactory(Config{Layout: hostlayout.Layout{WorkspaceSource: root, WorkingDirSource: root, Mounts: []hostlayout.Mount{{Source: root, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite}}}})
	if f.Name() != "none" {
		t.Errorf("expected name 'none', got %q", f.Name())
	}
	if !f.Available() {
		t.Error("expected Available to return true on Unix")
	}
	// Supported should accept any policy for none backend
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: t.TempDir()},
	}
	if err := f.Supported(policy); err != nil {
		t.Errorf("Supported: unexpected error: %v", err)
	}
}

func TestFactory_createSession(t *testing.T) {
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: t.TempDir(),
		},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
		InheritEnv: true,
	}
	f := NewFactory(Config{Layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir)})
	sess, err := f.CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	if sess.Policy().Filesystem.WorkingDir != sess.WorkingDir() {
		t.Error("Policy must match the provider execution view")
	}
	if !sess.Alive() {
		t.Error("expected Alive=true before close")
	}
}

func TestFactoryLayoutIsAuthoritativeAndCloned(t *testing.T) {
	workspace := t.TempDir()
	redirect := t.TempDir()
	layout := hostlayout.Layout{WorkspaceSource: workspace, WorkingDirSource: workspace, Mounts: []hostlayout.Mount{{Source: workspace, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite}}}
	factory := NewFactory(Config{Layout: layout})
	// Mutating the caller's slice after construction must not redirect a session.
	layout.Mounts[0].Source = redirect
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
		WorkingDir: redirect,
	}, Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll}, Env: map[string]string{"PRESERVED": "yes"}}
	session, err := factory.CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck
	if got := session.(*noneSession).WorkingDir(); got != workspace {
		t.Fatalf("WorkingDir = %q, want layout source %q", got, workspace)
	}
	got := session.Policy()
	if got.Filesystem.WorkingDir != workspace {
		t.Fatalf("Policy working directory = %q, want layout working directory %q", got.Filesystem.WorkingDir, workspace)
	}
	if got.Network.Mode != sandboxpkg.NetworkAllowAll || got.Env["PRESERVED"] != "yes" {
		t.Fatalf("Policy lost logical fields: %#v", got)
	}
}

func TestProjectFilesystemPathUsesDeepestLayoutMount(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "readonly")
	session := &noneSession{layout: hostlayout.Layout{
		WorkspaceSource: workspace, WorkingDirSource: workspace,
		// Deliberately broad-first: declaration order must not choose it.
		Mounts: []hostlayout.Mount{{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite}, {Source: nested, Target: sandboxpkg.PathUser, Access: hostlayout.ReadOnly}},
	}}
	if got, ok := session.ProjectFilesystemPath(filepath.Join(nested, "secret")); !ok || got != "/user/secret" {
		t.Fatalf("ProjectFilesystemPath = %q, %v; want deepest /user target", got, ok)
	}
}

func TestFactoryProjectsFilesystemEnvironmentPaths(t *testing.T) {
	workspace, user := t.TempDir(), t.TempDir()
	layout := hostlayout.Layout{WorkspaceSource: workspace, WorkingDirSource: workspace, Mounts: []hostlayout.Mount{{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite}, {Source: user, Target: sandboxpkg.PathUser, Access: hostlayout.ReadWrite}}}
	session, err := NewFactory(Config{Layout: layout}).CreateSession(context.Background(), sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: workspace}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck
	projector := session.(sandboxpkg.FilesystemPathProjector)
	for source, want := range map[string]string{
		session.Policy().Env["HOME"]:                                    sandboxpkg.PathWorkspace,
		session.Policy().Env[sandboxpkg.EnvStellaAssetsDir]:             sandboxpkg.PathUser + "/assets",
		filepath.Join(session.Policy().Env[sandboxpkg.EnvTempDir], "x"): sandboxpkg.PathTemp + "/x",
		sandboxpkg.PathWorkspace + "/a":                                 sandboxpkg.PathWorkspace + "/a",
	} {
		if got, ok := projector.ProjectFilesystemPath(source); !ok || got != want {
			t.Errorf("ProjectFilesystemPath(%q) = %q, %v; want %q", source, got, ok, want)
		}
	}
	for _, source := range []string{filepath.Join(filepath.Dir(workspace), "escape"), workspace + "-sibling", filepath.Join(workspace, "..", "escape"), "/workspace/../user", `/workspace\x`} {
		if _, ok := projector.ProjectFilesystemPath(source); ok {
			t.Errorf("ProjectFilesystemPath accepted %q", source)
		}
	}
}

func TestFilesystemUsesCanonicalPath(t *testing.T) {
	workspace := t.TempDir()
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
		WorkingDir: workspace,
	}}
	session, err := NewFactory(Config{Layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir)}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck
	fsSession, ok := session.(sandboxpkg.FilesystemSession)
	if !ok {
		t.Fatal("none session does not expose Filesystem")
	}
	filesystem, err := fsSession.Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close() //nolint:errcheck
	if err := filesystem.Write(context.Background(), "/workspace/file", strings.NewReader("ok"), sandboxpkg.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "file")); err != nil || string(got) != "ok" {
		t.Fatalf("file = %q, %v", got, err)
	}
}

func TestFilesystemCreatesMissingWritableRootButNotReadOnly(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace") // writable, not yet created
	readOnly := filepath.Join(base, "missing-ro") // read-only, not created
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
		WorkingDir: workspace,
	}}
	session, err := NewFactory(Config{Layout: layoutFor(workspace, workspace, hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite}, hostlayout.Mount{Source: readOnly, Target: sandboxpkg.PathUser, Access: hostlayout.ReadOnly})}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close() //nolint:errcheck
	fsSession := session.(sandboxpkg.FilesystemSession)
	// A missing read-only root must fail closed rather than be created.
	if _, err := fsSession.Filesystem(); err == nil {
		t.Fatal("Filesystem() must fail when a read-only mount root is missing")
	}
	if _, err := os.Stat(readOnly); !os.IsNotExist(err) {
		t.Fatalf("read-only mount root was created: %v", err)
	}
	// With only the writable mount, the root is materialized on demand.
	writableOnly := session.(*noneSession)
	writableOnly.layout.Mounts = writableOnly.layout.Mounts[:1]
	filesystem, err := fsSession.Filesystem()
	if err != nil {
		t.Fatalf("Filesystem() with writable mount: %v", err)
	}
	defer filesystem.Close() //nolint:errcheck
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		t.Fatalf("writable mount root not created: %v", err)
	}
}

func TestFactoryCreateSession_setsHostXDGPaths(t *testing.T) {
	workspace := t.TempDir()
	userData := t.TempDir()
	policy := sandboxpkg.Policy{
		Env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspace,
		},
	}
	sess, err := NewFactory(Config{Layout: layoutFor(workspace, workspace,
		hostlayout.Mount{Source: workspace, Target: sandboxpkg.PathWorkspace, Access: hostlayout.ReadWrite},
		hostlayout.Mount{Source: userData, Target: sandboxpkg.PathUser, Access: hostlayout.ReadWrite})}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	env := sess.Policy().Env
	for key, want := range map[string]string{
		"HOME":              workspace,
		"STELLA_ASSETS_DIR": filepath.Join(userData, "assets"),
		"XDG_CONFIG_HOME":   filepath.Join(userData, ".config"),
		"XDG_DATA_HOME":     filepath.Join(userData, ".local", "share"),
		"XDG_STATE_HOME":    filepath.Join(userData, ".local", "state"),
		"XDG_CACHE_HOME":    filepath.Join(userData, ".cache"),
	} {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want real host path %q", key, got, want)
		}
	}
	if _, ok := env["XDG_RUNTIME_DIR"]; ok {
		t.Error("XDG_RUNTIME_DIR must not be set")
	}
	if tmpDir := env[sandboxpkg.EnvTempDir]; tmpDir == "" {
		t.Error("TMPDIR must be set")
	} else if _, err := os.Stat(tmpDir); err != nil {
		t.Errorf("TMPDIR %q is unavailable: %v", tmpDir, err)
	}
}

func TestFactoryCreateSession_withoutUserDataFallsBackToWorkspace(t *testing.T) {
	workspace := t.TempDir()
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: workspace},
	}
	sess, err := NewFactory(Config{Layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir)}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	env := sess.Policy().Env
	for key, want := range map[string]string{
		"HOME":            workspace,
		"XDG_CONFIG_HOME": filepath.Join(workspace, ".config"),
		"XDG_DATA_HOME":   filepath.Join(workspace, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(workspace, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(workspace, ".cache"),
	} {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if _, ok := env["STELLA_ASSETS_DIR"]; ok {
		t.Error("STELLA_ASSETS_DIR must not be set")
	}
}

func TestFactoryCreateSession_errorRemovesOwnedTempDir(t *testing.T) {
	before, err := filepath.Glob(filepath.Join(os.TempDir(), "stella-none-session-tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]struct{}, len(before))
	for _, path := range before {
		known[path] = struct{}{}
	}
	if _, err := NewFactory().CreateSession(context.Background(), sandboxpkg.Policy{}); err == nil {
		t.Fatal("CreateSession accepted policy without a workspace")
	}
	after, err := filepath.Glob(filepath.Join(os.TempDir(), "stella-none-session-tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range after {
		if _, existed := known[path]; !existed {
			t.Errorf("CreateSession error leaked owned temp directory %q", path)
			_ = os.RemoveAll(path)
		}
	}
}

func TestFactoryCreateSession_ownsDistinctTempDirs(t *testing.T) {
	workspace := t.TempDir()
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: workspace}}
	first, err := NewFactory(Config{Layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir)}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession(first): %v", err)
	}
	second, err := NewFactory(Config{Layout: layoutFor(workspace, workspace)}).CreateSession(context.Background(), policy)
	if err != nil {
		first.Close() //nolint:errcheck
		t.Fatalf("CreateSession(second): %v", err)
	}
	firstTmp := first.Policy().Env[sandboxpkg.EnvTempDir]
	secondTmp := second.Policy().Env[sandboxpkg.EnvTempDir]
	if firstTmp == "" || secondTmp == "" || firstTmp == secondTmp {
		t.Fatalf("session temp dirs = %q and %q, want distinct non-empty paths", firstTmp, secondTmp)
	}
	toolPath := filepath.Join(firstTmp, "from-tool")
	if err := os.WriteFile(toolPath, []byte("tool"), 0o600); err != nil {
		t.Fatalf("write temp through file-tool path: %v", err)
	}
	result, err := first.Exec(context.Background(), `cat "$TMPDIR/from-tool"; printf exec > "$TMPDIR/from-exec"`, sandboxpkg.ExecOptions{})
	if err != nil || result.ExitCode != 0 || result.Stdout != "tool" {
		t.Fatalf("temp exec round trip = %+v, %v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(firstTmp, "from-exec")); err != nil || string(data) != "exec" {
		t.Fatalf("read exec temp through file-tool path = %q, %v", data, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}
	if _, err := os.Stat(firstTmp); !os.IsNotExist(err) {
		t.Errorf("first TMPDIR survives close: %v", err)
	}
	if _, err := os.Stat(secondTmp); err != nil {
		t.Errorf("closing first session affected second TMPDIR: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close(second): %v", err)
	}
	if _, err := os.Stat(secondTmp); !os.IsNotExist(err) {
		t.Errorf("second TMPDIR survives close: %v", err)
	}
}

func TestNoneSession_closeAndAlive(t *testing.T) {
	s := newTestSession(t)
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

func TestNoneSession_doneChanClosed(t *testing.T) {
	s := newTestSession(t)
	done := s.Done()
	_ = s.Close()
	select {
	case <-done:
	default:
		t.Error("done channel should be closed after Close()")
	}
}

func TestNoneSession_workingDir(t *testing.T) {
	tempDir := t.TempDir()
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: tempDir,
		},
	}
	s := &noneSession{
		id:     "test",
		policy: policy,
		layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir),
		done:   make(chan struct{}),
	}
	if s.WorkingDir() != tempDir {
		t.Errorf("WorkingDir = %q, want %q", s.WorkingDir(), tempDir)
	}
	if s.Policy().Filesystem.WorkingDir != tempDir {
		t.Error("Policy not preserved")
	}
}

func TestNoneSession_workingDirDefaultsToCwd(t *testing.T) {
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{},
	}
	s := &noneSession{
		id:     "test",
		policy: policy,
		layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir),
		done:   make(chan struct{}),
	}
	cwd, _ := os.Getwd()
	if s.WorkingDir() != cwd {
		t.Errorf("WorkingDir = %q, want %q", s.WorkingDir(), cwd)
	}
}

func TestResolvePath(t *testing.T) {
	s := newTestSession(t)
	tempDir := t.TempDir()
	s.policy.Filesystem.WorkingDir = tempDir
	s.layout.WorkingDirSource = tempDir

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "absolute path unchanged",
			input:    "/etc/passwd",
			expected: "/etc/passwd",
		},
		{
			name:     "relative path joined with working dir",
			input:    "file.txt",
			expected: filepath.Join(tempDir, "file.txt"),
		},
		{
			name:     "relative path with subdirs",
			input:    "subdir/nested/file.go",
			expected: filepath.Join(tempDir, "subdir", "nested", "file.go"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.resolvePath(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("ResolvePath(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestExec_success(t *testing.T) {
	s := newTestSession(t)
	result, err := s.Exec(context.Background(), "echo hello", sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("unexpected stdout: %q", result.Stdout)
	}
}

func TestExec_nonzeroExitCode(t *testing.T) {
	s := newTestSession(t)
	result, err := s.Exec(context.Background(), "exit 42", sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", result.ExitCode)
	}
}

func TestExec_withCwd(t *testing.T) {
	s := newTestSession(t)
	rawTempDir := t.TempDir()
	// Resolve symlinks for macOS /var → /private/var compatibility
	tempDir, err := filepath.EvalSymlinks(rawTempDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	opts := sandboxpkg.ExecOptions{Cwd: tempDir}
	result, err := s.Exec(context.Background(), "pwd", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	expected := tempDir + "\n"
	if result.Stdout != expected {
		t.Errorf("expected stdout %q, got %q", expected, result.Stdout)
	}
}

func TestExec_withEnv(t *testing.T) {
	s := newTestSession(t)
	opts := sandboxpkg.ExecOptions{
		Env: map[string]string{"TEST_VAR": "test_value"},
	}
	result, err := s.Exec(context.Background(), "echo $TEST_VAR", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "test_value\n" {
		t.Errorf("unexpected stdout: %q", result.Stdout)
	}
}

func TestExec_closedSession(t *testing.T) {
	s := newTestSession(t)
	_ = s.Close()
	_, err := s.Exec(context.Background(), "echo hello", sandboxpkg.ExecOptions{})
	if err == nil {
		t.Fatal("expected error for closed session, got nil")
	}
}

func TestStartProcess_success(t *testing.T) {
	s := newTestSession(t)
	req := sandboxpkg.ProcessRequest{
		Path: "cat",
		Args: []string{},
	}
	proc, err := s.StartProcess(context.Background(), req)
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	defer proc.Close() //nolint:errcheck

	if proc.PID() == 0 {
		t.Error("expected non-zero PID")
	}

	// Write to stdin and read from stdout
	_, err = proc.Stdin().Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write to stdin: %v", err)
	}
	_ = proc.Stdin().Close()

	result, err := proc.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestStartProcess_closedSession(t *testing.T) {
	s := newTestSession(t)
	_ = s.Close()
	req := sandboxpkg.ProcessRequest{Path: "echo", Args: []string{"hello"}}
	_, err := s.StartProcess(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for closed session, got nil")
	}
}

func TestBuildEnv(t *testing.T) {
	// Set test env vars.
	t.Setenv("TEST_HOST_VAR", "host_value")
	t.Setenv("STELLA_USER_DIR", "/host/stale-user")

	policy := sandboxpkg.Policy{
		Env:        map[string]string{"POLICY_VAR": "policy_value"},
		InheritEnv: true,
	}
	overrides := map[string]string{"OVERRIDE_VAR": "override_value", "STELLA_USER_DIR": "/override/stale-user"}

	env := buildEnv(policy, overrides)

	// Check that all expected vars are present
	var hasHost, hasPolicy, hasOverride bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "STELLA_USER_DIR=") {
			t.Fatalf("removed STELLA_USER_DIR must not be present: %q", kv)
		}
		switch kv {
		case "TEST_HOST_VAR=host_value":
			hasHost = true
		case "POLICY_VAR=policy_value":
			hasPolicy = true
		case "OVERRIDE_VAR=override_value":
			hasOverride = true
		}
	}

	if !hasHost {
		t.Error("expected TEST_HOST_VAR from host env")
	}
	if !hasPolicy {
		t.Error("expected POLICY_VAR from policy")
	}
	if !hasOverride {
		t.Error("expected OVERRIDE_VAR from overrides")
	}
}

func TestBuildEnv_noInherit(t *testing.T) {
	t.Setenv("TEST_HOST_VAR", "host_value")

	policy := sandboxpkg.Policy{
		Env:        map[string]string{"POLICY_VAR": "policy_value"},
		InheritEnv: false,
	}

	env := buildEnv(policy, nil)

	// Check that host var is NOT present
	for _, kv := range env {
		if kv == "TEST_HOST_VAR=host_value" {
			t.Error("TEST_HOST_VAR should not be present when InheritEnv is false")
		}
	}

	// But policy var should be present
	var hasPolicy bool
	for _, kv := range env {
		if kv == "POLICY_VAR=policy_value" {
			hasPolicy = true
		}
	}
	if !hasPolicy {
		t.Error("expected POLICY_VAR from policy")
	}
}

// newTestSession returns a noneSession with a temporary working directory.
func newTestSession(t *testing.T) *noneSession {
	t.Helper()
	root := t.TempDir()
	policy := sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: root}}
	return &noneSession{
		id:     "test",
		policy: policy,
		layout: layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir),
		done:   make(chan struct{}),
	}
}
