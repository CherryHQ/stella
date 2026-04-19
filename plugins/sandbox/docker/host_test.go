package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// TestToContainerPath verifies host→container path translation.
func TestToContainerPath(t *testing.T) {
	mounts := []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: "/workspace"},
		{HostPath: "/host/ro", ContainerPath: "/workspace-readonly/0", ReadOnly: true},
	}

	cases := []struct {
		hostPath      string
		wantContainer string
		wantErr       bool
	}{
		{"/host/workspace", "/workspace", false},
		{"/host/workspace/", "/workspace", false},
		{"/host/workspace/src/main.go", "/workspace/src/main.go", false},
		{"/host/ro/lib.txt", "/workspace-readonly/0/lib.txt", false},
		{"/host/ro", "/workspace-readonly/0", false},
		{"/host/other/path", "", true},
		{"/host/workspac", "", true}, // not under /host/workspace (just a prefix)
	}

	for _, tc := range cases {
		hostPath := filepath.Clean(tc.hostPath)
		got, err := toContainerPath(mounts, hostPath)
		if tc.wantErr {
			if err == nil {
				t.Errorf("toContainerPath(%q): expected error, got %q", tc.hostPath, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("toContainerPath(%q): unexpected error: %v", tc.hostPath, err)
			continue
		}
		if got != tc.wantContainer {
			t.Errorf("toContainerPath(%q): got %q, want %q", tc.hostPath, got, tc.wantContainer)
		}
	}
}

// TestToContainerPath_DeepestMatch verifies longest-prefix (deepest) mount wins.
func TestToContainerPath_DeepestMount(t *testing.T) {
	mounts := []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: "/workspace"},
		{HostPath: "/host/workspace/sub", ContainerPath: "/workspace-sub"},
	}

	got, err := toContainerPath(mounts, "/host/workspace/sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/workspace-sub/file.txt" {
		t.Errorf("expected deepest mount to win, got %q", got)
	}
}

// TestDockerHostResolvePath verifies relative paths are joined to WorkingDir.
func TestDockerHostResolvePath(t *testing.T) {
	dir := t.TempDir()
	policy := Policy{
		Filesystem: FilesystemPolicy{
			WorkspaceRoot: dir,
			WorkingDir:    dir,
		},
	}
	session := &dockerSession{
		id:     "test-session",
		policy: policy,
		done:   make(chan struct{}),
	}
	host := &dockerHost{session: session}

	abs, err := host.ResolvePath("relative/path")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want := filepath.Join(dir, "relative/path")
	if abs != want {
		t.Errorf("got %q, want %q", abs, want)
	}

	// Absolute path passes through unchanged.
	abs2, err := host.ResolvePath("/absolute/path")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if abs2 != "/absolute/path" {
		t.Errorf("got %q, want %q", abs2, "/absolute/path")
	}
}

// TestTranslateEnvPaths verifies that absolute host paths are translated to
// container paths and non-mounted paths are dropped.
func TestTranslateEnvPaths(t *testing.T) {
	mounts := []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: "/workspace"},
	}

	env := map[string]string{
		"HOME":      "/host/workspace",   // mounted — should translate
		"ANNA_HOME": "/host/.anna",       // not mounted — should be dropped
		"TERM":      "xterm-256color",    // non-path — should pass through
		"LANG":      "en_US.UTF-8",       // non-path — should pass through
	}

	got := translateEnvPaths(env, mounts)

	if got["HOME"] != "/workspace" {
		t.Errorf("HOME: got %q, want /workspace", got["HOME"])
	}
	if _, ok := got["ANNA_HOME"]; ok {
		t.Errorf("ANNA_HOME should be dropped (not in any mount), got %q", got["ANNA_HOME"])
	}
	if got["TERM"] != "xterm-256color" {
		t.Errorf("TERM: got %q, want xterm-256color", got["TERM"])
	}
	if got["LANG"] != "en_US.UTF-8" {
		t.Errorf("LANG: got %q, want en_US.UTF-8", got["LANG"])
	}
}

// TestDockerHostReadFile verifies that ReadFile reads from the host filesystem.
func TestDockerHostReadFile(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello docker sandbox")
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	policy := Policy{
		Filesystem: FilesystemPolicy{
			WorkspaceRoot: dir,
			WorkingDir:    dir,
		},
	}
	session := &dockerSession{
		id:     "test-session",
		policy: policy,
		done:   make(chan struct{}),
	}
	host := &dockerHost{session: session}

	result, err := host.ReadFile(nil, filepath.Join(dir, "test.txt"), 0, 0) //nolint:staticcheck
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(result.Content) != string(content) {
		t.Errorf("ReadFile: got %q, want %q", result.Content, content)
	}
}
