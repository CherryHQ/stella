package none

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestFactory_basics(t *testing.T) {
	f := NewFactory()
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
	f := NewFactory()
	sess, err := f.CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	if sess.Policy().Filesystem.WorkingDir != policy.Filesystem.WorkingDir {
		t.Error("Policy not preserved")
	}
	if !sess.Alive() {
		t.Error("expected Alive=true before close")
	}
}

func TestFactoryCreateSession_setsHostXDGPaths(t *testing.T) {
	workspace := t.TempDir()
	userData := t.TempDir()
	policy := sandboxpkg.Policy{
		Env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
			Mounts: []sandboxpkg.Mount{
				{HostPath: workspace, SandboxPath: sandboxpkg.MountWorkspace, Access: sandboxpkg.MountReadWrite},
				{HostPath: userData, SandboxPath: sandboxpkg.MountUserData, Access: sandboxpkg.MountReadWrite},
			},
		},
	}
	sess, err := NewFactory().CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	env := sess.Policy().Env
	for key, want := range map[string]string{
		"HOME":            workspace,
		"STELLA_USER_DIR": userData,
		"XDG_CONFIG_HOME": filepath.Join(userData, ".config"),
		"XDG_DATA_HOME":   filepath.Join(userData, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(userData, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(userData, ".cache"),
	} {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want real host path %q", key, got, want)
		}
	}
	if _, ok := env["XDG_RUNTIME_DIR"]; ok {
		t.Error("XDG_RUNTIME_DIR must not be set")
	}
}

func TestFactoryCreateSession_withoutUserDataFallsBackToWorkspace(t *testing.T) {
	workspace := t.TempDir()
	sess, err := NewFactory().CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
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
	if _, ok := env["STELLA_USER_DIR"]; ok {
		t.Error("STELLA_USER_DIR must not be set")
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
			got, err := s.ResolvePath(tc.input)
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
	// Set a test env var
	t.Setenv("TEST_HOST_VAR", "host_value")

	policy := sandboxpkg.Policy{
		Env:        map[string]string{"POLICY_VAR": "policy_value"},
		InheritEnv: true,
	}
	overrides := map[string]string{"OVERRIDE_VAR": "override_value"}

	env := buildEnv(policy, overrides)

	// Check that all expected vars are present
	var hasHost, hasPolicy, hasOverride bool
	for _, kv := range env {
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
	return &noneSession{
		id:     "test",
		policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: t.TempDir()}},
		done:   make(chan struct{}),
	}
}
