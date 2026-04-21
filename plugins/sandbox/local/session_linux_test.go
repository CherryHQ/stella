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
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
	}

	path, args, err := wrapCommand(policy, "sh", []string{"-c", "echo hi"})
	if err != nil {
		// Neither tool is available — this is an expected failure mode.
		if strings.Contains(err.Error(), "neither bwrap nor unshare") {
			t.Skipf("neither bwrap nor unshare available: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}

	bwrapAvail := exec.LookPath("bwrap") == nil
	unshareAvail := exec.LookPath("unshare") == nil

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

// TestWrapCommand_linux_allowAllNoWrap verifies that when network is allow_all
// and bwrap is unavailable, the command runs unwrapped (or with bwrap without
// --unshare-net).
func TestWrapCommand_linux_allowAllNoWrap(t *testing.T) {
	root := "/tmp/test-workspace"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}

	_, args, err := wrapCommand(policy, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error for allow_all network: %v", err)
	}

	// When bwrap is used, --unshare-net must NOT appear.
	for _, a := range args {
		if a == "--unshare-net" {
			t.Errorf("--unshare-net should not appear for allow_all network, got args %v", args)
		}
	}
}
