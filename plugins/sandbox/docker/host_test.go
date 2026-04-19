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

// TestDockerHostResolvePath verifies relative paths are joined to WorkingDir,
// absolute paths inside the mount set are passed through, and absolute paths
// outside any mount are rejected.
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
		mountTable: []dockerclient.Mount{
			{HostPath: dir, ContainerPath: "/workspace"},
		},
		done: make(chan struct{}),
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

	// Absolute path inside a mounted tree passes through unchanged.
	insideMount := filepath.Join(dir, "sub/file.txt")
	got, err := host.ResolvePath(insideMount)
	if err != nil {
		t.Fatalf("ResolvePath inside mount: %v", err)
	}
	if got != insideMount {
		t.Errorf("got %q, want %q", got, insideMount)
	}

	// Absolute path outside every mount must be rejected so filesystem
	// policies (workspace-only, read-only trees) cannot be bypassed.
	if _, err := host.ResolvePath("/absolute/path"); err == nil {
		t.Error("ResolvePath: expected error for path outside mount set")
	}
}

// TestTranslateEnvPaths verifies that mounted absolute paths translate to
// their container paths, non-mounted absolute paths drop, host-only keys
// (PATH, HOME) drop wholesale so the image's baked values stand, and
// non-path values pass through.
func TestTranslateEnvPaths(t *testing.T) {
	mounts := []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: "/workspace"},
	}

	env := map[string]string{
		"PATH":        "/host/tools/bin:/usr/bin", // host-only — should drop
		"HOME":        "/host/workspace",          // host-only — should drop even if mounted
		"ANNA_HOME":   "/host/.anna",              // absolute, not mounted — should drop
		"WORKING_DIR": "/host/workspace",          // mounted — should translate
		"TERM":        "xterm-256color",           // non-path — pass through
		"LANG":        "en_US.UTF-8",              // non-path — pass through
	}

	got := translateEnvPaths(env, mounts)

	for _, k := range []string{"PATH", "HOME", "ANNA_HOME"} {
		if v, ok := got[k]; ok {
			t.Errorf("%s should be dropped, got %q", k, v)
		}
	}
	if got["WORKING_DIR"] != "/workspace" {
		t.Errorf("WORKING_DIR: got %q, want /workspace", got["WORKING_DIR"])
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
		mountTable: []dockerclient.Mount{
			{HostPath: dir, ContainerPath: "/workspace"},
		},
		done: make(chan struct{}),
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
