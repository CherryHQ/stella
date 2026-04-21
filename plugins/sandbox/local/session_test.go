package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

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
		realRoot:    "/home/anna/.anna-dev/workspaces/1",
	}

	tests := []struct {
		in   string
		want string
	}{
		{"/workspace/foo.go", "/home/anna/.anna-dev/workspaces/1/foo.go"},
		{"/workspace/sub/dir/file.go", "/home/anna/.anna-dev/workspaces/1/sub/dir/file.go"},
		// Exact root
		{"/workspace", "/home/anna/.anna-dev/workspaces/1"},
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

// TestExec_nonzeroExitCode verifies that Exec returns a non-zero ExitCode for
// failing commands and does not surface it as a Go error.
func TestExec_nonzeroExitCode(t *testing.T) {
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
