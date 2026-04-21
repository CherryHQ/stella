package local

import (
	"os/exec"
	"strings"
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// TestWrapCommand_linux_bwrapOrUnshare verifies that wrapCommand on Linux
// uses bwrap or unshare for network isolation when those tools are available.
func TestWrapCommand_linux_bwrapOrUnshare(t *testing.T) {
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
	if err != nil {
		// Neither tool is available — this is an expected failure mode.
		if strings.Contains(err.Error(), "neither bwrap nor unshare") {
			t.Skipf("neither bwrap nor unshare available: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}

	_, bwrapErr := exec.LookPath("bwrap")
	bwrapAvail := bwrapErr == nil
	_, unshareErr := exec.LookPath("unshare")
	unshareAvail := unshareErr == nil

	if bwrapAvail {
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
	} else if unshareAvail {
		if !strings.HasSuffix(path, "unshare") {
			t.Errorf("expected unshare executable, got %q", path)
		}
		if len(args) == 0 || args[0] != "--net" {
			t.Errorf("expected --net as first arg to unshare, got %v", args)
		}
	}
}

// TestWrapCommand_linux_allowAllNoWrap verifies that when network is allow_all,
// the command runs with bwrap (for /workspace remapping) but without
// --unshare-net, or fully unwrapped when bwrap is absent.
func TestWrapCommand_linux_allowAllNoWrap(t *testing.T) {
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

	// Whether bwrap is used or not, --unshare-net must NOT appear.
	for _, a := range args {
		if a == "--unshare-net" {
			t.Errorf("--unshare-net should not appear for allow_all network, got args %v", args)
		}
	}
}

// TestWrapCommand_linux_bwrapWorkspaceRemap verifies that when bwrap is
// available, the args include --dir /workspace, --bind <realRoot> /workspace,
// and --chdir <sandboxCwd>.
func TestWrapCommand_linux_bwrapWorkspaceRemap(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not available")
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

	hasFlag := func(flag, val string) bool {
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == val {
				return true
			}
		}
		return false
	}
	hasSingle := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}

	if !hasSingle("--dir") {
		t.Error("expected --dir in bwrap args")
	}
	if !hasFlag("--dir", "/workspace") {
		t.Errorf("expected --dir /workspace in bwrap args, got %v", args)
	}
	if !hasFlag("--bind", root) {
		t.Errorf("expected --bind %s in bwrap args, got %v", root, args)
	}
	if !hasFlag("--chdir", sandboxCwd) {
		t.Errorf("expected --chdir %s in bwrap args, got %v", sandboxCwd, args)
	}
	if hostCwd != root {
		t.Errorf("expected hostCwd %q, got %q", root, hostCwd)
	}
}
