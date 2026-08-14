package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
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

// TestDockerProviderResolver verifies relative paths are joined to the canonical
// WorkingDir and physical coordinates supplied by upper layers are rejected.
func TestDockerProviderResolver(t *testing.T) {
	dir := t.TempDir()
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: "/workspace",
		},
	}
	session := &dockerSession{
		id:     "test-session",
		policy: policy,
		done:   make(chan struct{}),
	}
	attachDockerTestFiles(t, session, []sessionfs.Mount{{HostPath: dir, SandboxPath: "/workspace"}})

	resolved, err := session.resolver.Resolve("relative/path", false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(dir, "relative/path")
	if got := resolved.HostPath(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	resolved, err = session.resolver.Resolve("/workspace/sub/file.txt", false)
	if err != nil {
		t.Fatalf("Resolve inside mount: %v", err)
	}
	if got, want := resolved.HostPath(), filepath.Join(dir, "sub/file.txt"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if _, err := session.resolver.Resolve(dir, false); err == nil {
		t.Error("Resolve accepted a provider physical coordinate")
	}
	if _, err := session.resolver.Resolve("/absolute/path", false); err == nil {
		t.Error("Resolve accepted a canonical path outside the mount set")
	}
}

// TestDockerFileAccessConfinesSymlinks verifies rooted operations may follow an
// in-root symlink but cannot escape through a leaf or ancestor symlink.
func TestDockerFileAccessConfinesSymlinks(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: "/workspace",
		},
	}
	session := &dockerSession{
		id:     "test-session",
		policy: policy,
		done:   make(chan struct{}),
	}
	attachDockerTestFiles(t, session, []sessionfs.Mount{{HostPath: workspace, SandboxPath: "/workspace"}})

	regular := filepath.Join(workspace, "regular.txt")
	if err := os.WriteFile(regular, []byte("ok"), 0o644); err != nil {
		t.Fatalf("seed regular: %v", err)
	}
	if got, err := session.Files().ReadFile("regular.txt"); err != nil || string(got) != "ok" {
		t.Fatalf("regular file: %v", err)
	}

	if err := session.Files().WriteFile("newfile.txt", []byte("new"), 0o600); err != nil {
		t.Fatalf("non-existent leaf in clean dir: %v", err)
	}

	linkOut := filepath.Join(workspace, "leak")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), linkOut); err != nil {
		t.Fatalf("seed outward symlink: %v", err)
	}
	if _, err := session.Files().ReadFile("leak"); err == nil {
		t.Error("expected rejection for leaf symlink pointing outside mount")
	}

	if err := os.Symlink("regular.txt", filepath.Join(workspace, "inside-link")); err != nil {
		t.Fatalf("seed inward symlink: %v", err)
	}
	if got, err := session.Files().ReadFile("inside-link"); err != nil || string(got) != "ok" {
		t.Errorf("in-root symlink read = %q, %v", got, err)
	}

	dirlink := filepath.Join(workspace, "dirlink")
	if err := os.Symlink(outside, dirlink); err != nil {
		t.Fatalf("seed ancestor symlink: %v", err)
	}
	if err := session.Files().WriteFile("dirlink/new.txt", []byte("escape"), 0o600); err == nil {
		t.Error("expected rejection for write through symlinked ancestor")
	}

	// After removing the symlinks, the same names work as regular files.
	for _, name := range []string{"leak", "inside-link", "dirlink"} {
		if err := os.Remove(filepath.Join(workspace, name)); err != nil {
			t.Fatalf("cleanup %s: %v", name, err)
		}
	}
	if err := session.Files().WriteFile("leak", []byte("safe"), 0o600); err != nil {
		t.Fatalf("non-existent leaf after cleanup: %v", err)
	}
}

func attachDockerTestFiles(t *testing.T, session *dockerSession, mounts []sessionfs.Mount) {
	t.Helper()
	resolver, err := sessionfs.NewResolver(session.policy.Filesystem.WorkingDir, mounts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	session.resolver = resolver
	session.files = sessionfs.NewAccess(resolver)
}

func TestDockerPromptProjectContextUsesCanonicalWorkingDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("canonical docker instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &dockerSession{
		id:     "prompt-canonical",
		policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: "/workspace"}},
		done:   make(chan struct{}),
	}
	s.host = &dockerHost{session: s}
	attachDockerTestFiles(t, s, []sessionfs.Mount{{HostPath: root, SandboxPath: "/workspace"}})

	got := prompt.BuildSystemPromptFromDB(context.Background(), prompt.DBPromptParams{
		SystemPrompt: "You are Stella.",
		Session:      s,
	})
	if !strings.Contains(got, "canonical docker instructions") {
		t.Fatalf("prompt did not discover AGENTS.md through canonical project view: %s", got)
	}
	if _, err := s.Files().ReadFile(filepath.Join(root, "AGENTS.md")); err == nil {
		t.Fatal("resolver accepted the physical ProjectRoot coordinate")
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
		"PATH":              "/host/tools/bin:/usr/bin", // host-only — should drop
		"STELLA_USER_DIR":   "/host/data",               // removed — always drop
		"HOME":              "/host/workspace",          // mounted — should translate
		"XDG_CONFIG_HOME":   "/host/data/.config",
		"XDG_DATA_HOME":     "/host/data/.local/share",
		"XDG_STATE_HOME":    "/host/data/.local/state",
		"XDG_CACHE_HOME":    "/host/data/.cache",
		"STELLA_HOME":       "/host/.stella",     // envMap — should translate
		"STELLA_ASSETS_DIR": "/host/data/assets", // mounted at /user — should translate
		"TMPDIR":            "/host/tmp",         // mounted at /tmp — should translate
		"WORKING_DIR":       "/host/workspace",   // unknown key — absolute-looking literal
		"ABSOLUTE_SECRET":   "/host/secret",      // unknown key — must remain literal
		"MISE_CACHE_DIR":    "/outside/cache",    // declared path — unmapped, so drop
		"TERM":              "xterm-256color",    // non-path — pass through
		"LANG":              "en_US.UTF-8",       // non-path — pass through
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
	if got["TERM"] != "xterm-256color" {
		t.Errorf("TERM: got %q, want xterm-256color", got["TERM"])
	}
	if got["LANG"] != "en_US.UTF-8" {
		t.Errorf("LANG: got %q, want en_US.UTF-8", got["LANG"])
	}
}
