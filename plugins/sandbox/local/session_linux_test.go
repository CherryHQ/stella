package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// TestWrapCommand_linux_networkDisabled verifies that wrapCommand on Linux
// uses bwrap with --unshare-net when network is disabled, or returns an error
// when bwrap is not functional (no fallback to unshare).
func TestWrapCommand_linux_networkDisabled(t *testing.T) {
	root := "/tmp/test-workspace"
	sandboxCwd := "/workspace"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
	}

	path, args, _, err := wrapCommand(policy, sandboxCwd, "sh", []string{"-c", "echo hi"})

	if !bwrapFunctional() {
		if err == nil {
			t.Fatal("expected error when bwrap is not functional, got nil")
		}
		if !strings.Contains(err.Error(), "bwrap") {
			t.Errorf("error should mention bwrap, got: %v", err)
		}
		return
	}

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "bwrap") {
		t.Errorf("expected bwrap executable, got %q", path)
	}
	found := false
	for _, a := range args {
		if a == "--unshare-net" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --unshare-net in bwrap args, got %v", args)
	}
}

// TestWrapCommand_linux_allowAllNoWrap verifies that when network is allow_all,
// bwrap is used without --unshare-net.
func TestWrapCommand_linux_allowAllNoWrap(t *testing.T) {
	if !bwrapFunctional() {
		t.Skip("bwrap not functional")
	}

	root := "/tmp/test-workspace"
	sandboxCwd := "/workspace"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	_, args, _, err := wrapCommand(policy, sandboxCwd, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error for allow_all network: %v", err)
	}

	for _, a := range args {
		if a == "--unshare-net" {
			t.Errorf("--unshare-net should not appear for allow_all network, got args %v", args)
		}
	}
}

// TestWrapCommand_linux_bwrapWorkspaceRemap verifies that when bwrap is
// available, the args include --dir /workspace, --bind <realRoot> /workspace,
// and --chdir <sandboxCwd>.
func TestLocalExec_linux_workspaceWritableOutsideHidden(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if !bwrapFunctional() {
		t.Skip("bwrap not functional (namespace creation blocked)")
	}

	rawRoot := t.TempDir()
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}

	s := &localSession{
		id:          "test",
		policy: sandboxpkg.Policy{
			Filesystem: sandboxpkg.FilesystemPolicy{WorkspaceRoot: root, WorkingDir: root},
			Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
			Env: map[string]string{
				"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME": "/workspace",
			},
		},
		realRoot:    root,
		sandboxRoot: "/workspace",
		done:        make(chan struct{}),
	}

	cmd := "echo ok > /workspace/out.txt && test ! -e " + shellQuote(outsideFile) + " && cat /workspace/out.txt"
	result, err := s.Exec(context.Background(), cmd, sandboxpkg.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v (stderr=%q)", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("stdout=%q, want ok", result.Stdout)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestAppendResolvedFileMount_mountsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	link := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(target, []byte("nameserver 127.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	args := appendResolvedFileMount(nil, link)
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--ro-bind\x00"+target+"\x00"+target) {
		t.Fatalf("expected resolved target bind for %q, got %v", target, args)
	}
}

func TestWrapCommand_linux_bwrapWorkspaceRemap(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if !bwrapFunctional() {
		t.Skip("bwrap not functional (namespace creation blocked)")
	}

	root := "/tmp/test-workspace"
	sandboxCwd := "/workspace/sub"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    sandboxCwd,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	execPath, args, hostCwd, err := wrapCommand(policy, sandboxCwd, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(execPath, "bwrap") {
		t.Errorf("expected bwrap, got %q", execPath)
	}

	flagIndex := func(flag string) int {
		for i, a := range args {
			if a == flag {
				return i
			}
		}
		return -1
	}
	hasFlag := func(flag, val string) bool {
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == val {
				return true
			}
		}
		return false
	}
	hasFlagPair := func(flag, first, second string) bool {
		for i, a := range args {
			if a == flag && i+2 < len(args) && args[i+1] == first && args[i+2] == second {
				return true
			}
		}
		return false
	}
	hasSingle := func(flag string) bool {
		return flagIndex(flag) >= 0
	}

	if !hasSingle("--dir") {
		t.Error("expected --dir in bwrap args")
	}
	if !hasFlag("--dir", "/workspace") {
		t.Errorf("expected --dir /workspace in bwrap args, got %v", args)
	}
	if !hasFlag("--tmpfs", "/dev/shm") {
		t.Errorf("expected --tmpfs /dev/shm for common Linux IPC/tmp users, got %v", args)
	}
	if !hasFlag("--tmpfs", "/var/tmp") {
		t.Errorf("expected --tmpfs /var/tmp for common Linux temp users, got %v", args)
	}
	if !hasFlagPair("--bind", root, "/workspace") {
		t.Errorf("expected --bind %s /workspace in bwrap args, got %v", root, args)
	}
	if hasFlagPair("--ro-bind", "/", "/") {
		t.Errorf("local bwrap must not expose the whole host root read-only, got %v", args)
	}
	roBindIndex := flagIndex("--ro-bind")
	bindIndex := flagIndex("--bind")
	if roBindIndex < 0 || bindIndex < 0 || roBindIndex > bindIndex {
		t.Errorf("expected read-only runtime mounts before writable --bind %s /workspace, got %v", root, args)
	}
	if !hasFlag("--chdir", sandboxCwd) {
		t.Errorf("expected --chdir %s in bwrap args, got %v", sandboxCwd, args)
	}
	if hostCwd != root {
		t.Errorf("expected hostCwd %q, got %q", root, hostCwd)
	}
}
