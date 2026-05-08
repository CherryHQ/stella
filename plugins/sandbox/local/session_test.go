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
