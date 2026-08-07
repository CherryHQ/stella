package docker

import (
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
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
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: dir,
		},
	}
	session := &dockerSession{
		id:         "test-session",
		policy:     policy,
		workingDir: "/workspace",
		mountTable: []dockerclient.Mount{
			{HostPath: dir, ContainerPath: "/workspace"},
		},
		done: make(chan struct{}),
	}
	host := &dockerHost{session: session}

	abs, err := host.resolvePath("relative/path")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want := filepath.Join(dir, "relative/path")
	if abs != want {
		t.Errorf("got %q, want %q", abs, want)
	}

	// Absolute path inside a mounted tree passes through unchanged.
	insideMount := filepath.Join(dir, "sub/file.txt")
	got, err := host.resolvePath(insideMount)
	if err != nil {
		t.Fatalf("ResolvePath inside mount: %v", err)
	}
	if got != insideMount {
		t.Errorf("got %q, want %q", got, insideMount)
	}

	// Absolute path outside every mount must be rejected so filesystem
	// policies (workspace-only, read-only trees) cannot be bypassed.
	if _, err := host.resolvePath("/absolute/path"); err == nil {
		t.Error("ResolvePath: expected error for path outside mount set")
	}
}

// TestDockerHostResolvePath_RejectsSymlinks verifies that any symlink in
// the resolved path is rejected, closing the escape where an agent
// creates a symlink in the workspace (via the sandbox-routed bash tool)
// and then reads/writes/edits through it from the stella-process side.
func TestDockerHostResolvePath_RejectsSymlinks(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: workspace,
		},
	}
	session := &dockerSession{
		id:         "test-session",
		policy:     policy,
		workingDir: "/workspace",
		mountTable: []dockerclient.Mount{
			{HostPath: workspace, ContainerPath: "/workspace"},
		},
		done: make(chan struct{}),
	}
	host := &dockerHost{session: session}

	// Baseline: a regular file in the workspace resolves normally.
	regular := filepath.Join(workspace, "regular.txt")
	if err := os.WriteFile(regular, []byte("ok"), 0o644); err != nil {
		t.Fatalf("seed regular: %v", err)
	}
	if _, err := host.resolvePath("regular.txt"); err != nil {
		t.Fatalf("regular file: %v", err)
	}

	// Baseline: a non-existent leaf in a clean directory resolves — writes
	// of new files must still work.
	if _, err := host.resolvePath("newfile.txt"); err != nil {
		t.Fatalf("non-existent leaf in clean dir: %v", err)
	}

	// Leaf is a symlink pointing outside the mount → reject.
	linkOut := filepath.Join(workspace, "leak")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), linkOut); err != nil {
		t.Fatalf("seed outward symlink: %v", err)
	}
	if _, err := host.resolvePath("leak"); err == nil {
		t.Error("expected rejection for leaf symlink pointing outside mount")
	}

	// Leaf is a symlink pointing inside the mount → still rejected, by
	// policy. Legit code does not create symlinks in a session workspace;
	// the strict rule keeps the escape surface zero.
	if err := os.Symlink(regular, filepath.Join(workspace, "inside-link")); err != nil {
		t.Fatalf("seed inward symlink: %v", err)
	}
	if _, err := host.resolvePath("inside-link"); err == nil {
		t.Error("expected rejection for any leaf symlink, even inside mount")
	}

	// Ancestor directory is a symlink → reject writes through it. This
	// catches the "ln -s /outside dirlink && write dirlink/new.txt" case.
	dirlink := filepath.Join(workspace, "dirlink")
	if err := os.Symlink(outside, dirlink); err != nil {
		t.Fatalf("seed ancestor symlink: %v", err)
	}
	if _, err := host.resolvePath("dirlink/new.txt"); err == nil {
		t.Error("expected rejection for write through symlinked ancestor")
	}

	// After removing the symlinks, the same names work as regular files.
	for _, name := range []string{"leak", "inside-link", "dirlink"} {
		if err := os.Remove(filepath.Join(workspace, name)); err != nil {
			t.Fatalf("cleanup %s: %v", name, err)
		}
	}
	if _, err := host.resolvePath("leak"); err != nil {
		t.Fatalf("non-existent leaf after cleanup: %v", err)
	}
}

// TestTranslateEnvPaths verifies that mounted absolute paths translate to
// their container paths, non-mounted absolute paths drop, host-only PATH drops,
// and HOME plus persistent XDG paths translate to the container filesystem view.
func TestTranslateEnvPaths(t *testing.T) {
	mounts := []dockerclient.Mount{
		{HostPath: "/host/workspace", ContainerPath: "/workspace"},
		{HostPath: "/host/data", ContainerPath: "/user"},
		{HostPath: "/host/tmp", ContainerPath: "/tmp"},
		{HostPath: "/host/.stella/bin", ContainerPath: "/home/stella/.stella/bin", ReadOnly: true},
		{HostPath: "/host/.stella/skills", ContainerPath: "/home/stella/.stella/skills", ReadOnly: true},
	}
	envMaps := []envPathMap{
		{HostPrefix: "/host/.stella", ContainerPrefix: "/home/stella/.stella"},
	}

	env := map[string]string{
		"PATH":                     "/host/tools/bin:/usr/bin", // host-only — should drop
		"STELLA_USER_DIR":          "/host/data",               // removed — always drop
		"HOME":                     "/host/workspace",          // mounted — should translate
		"XDG_CONFIG_HOME":          "/host/data/.config",
		"XDG_DATA_HOME":            "/host/data/.local/share",
		"XDG_STATE_HOME":           "/host/data/.local/state",
		"XDG_CACHE_HOME":           "/host/data/.cache",
		"STELLA_HOME":              "/host/.stella",     // envMap — should translate
		"STELLA_ASSETS_DIR":        "/host/data/assets", // mounted at /user — should translate
		"TMPDIR":                   "/host/tmp",         // mounted at /tmp — should translate
		"WORKING_DIR":              "/host/workspace",   // unknown key — absolute-looking literal
		"ABSOLUTE_SECRET":          "/host/secret",      // unknown key — must remain literal
		"MISE_CACHE_DIR":           "/outside/cache",    // declared path — unmapped, so drop
		"LARKSUITE_CLI_CONFIG_DIR": "/host/data/.lark-cli",
		"LARKSUITE_CLI_DATA_DIR":   "/host/data/.lark-cli/data",
		"TERM":                     "xterm-256color", // non-path — pass through
		"LANG":                     "en_US.UTF-8",    // non-path — pass through
	}

	got := translateEnvPaths(env, mounts, envMaps)

	for key, want := range map[string]string{
		"STELLA_ASSETS_DIR": "/user/assets",
		"TMPDIR":            "/tmp",
	} {
		if got[key] != want {
			t.Errorf("%s: got %q, want %q", key, got[key], want)
		}
	}

	for _, key := range []string{"PATH", "STELLA_USER_DIR"} {
		if value, ok := got[key]; ok {
			t.Errorf("%s should be dropped, got %q", key, value)
		}
	}
	for key, want := range map[string]string{
		"HOME":            "/workspace",
		"XDG_CONFIG_HOME": "/user/.config",
		"XDG_DATA_HOME":   "/user/.local/share",
		"XDG_STATE_HOME":  "/user/.local/state",
		"XDG_CACHE_HOME":  "/user/.cache",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if got["STELLA_HOME"] != "/home/stella/.stella" {
		t.Errorf("STELLA_HOME: got %q, want /home/stella/.stella", got["STELLA_HOME"])
	}
	if got["WORKING_DIR"] != "/host/workspace" || got["ABSOLUTE_SECRET"] != "/host/secret" {
		t.Errorf("unknown absolute-looking literals changed: WORKING_DIR=%q ABSOLUTE_SECRET=%q", got["WORKING_DIR"], got["ABSOLUTE_SECRET"])
	}
	if _, ok := got["MISE_CACHE_DIR"]; ok {
		t.Errorf("unmapped declared host path must be dropped, got %q", got["MISE_CACHE_DIR"])
	}
	if got["LARKSUITE_CLI_CONFIG_DIR"] != "/user/.lark-cli" {
		t.Errorf("LARKSUITE_CLI_CONFIG_DIR: got %q, want /user/.lark-cli", got["LARKSUITE_CLI_CONFIG_DIR"])
	}
	if got["LARKSUITE_CLI_DATA_DIR"] != "/user/.lark-cli/data" {
		t.Errorf("LARKSUITE_CLI_DATA_DIR: got %q, want /user/.lark-cli/data", got["LARKSUITE_CLI_DATA_DIR"])
	}
	if got["TERM"] != "xterm-256color" {
		t.Errorf("TERM: got %q, want xterm-256color", got["TERM"])
	}
	if got["LANG"] != "en_US.UTF-8" {
		t.Errorf("LANG: got %q, want en_US.UTF-8", got["LANG"])
	}
}
