package local

import (
	"os/exec"
	"reflect"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestWrapCommand_darwin_passthrough(t *testing.T) {
	root := "/tmp/test-workspace"
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
	}

	execPath, args, hostCwd, err := wrapCommand(policy, root, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantExecPath, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("look up sh: %v", err)
	}
	if execPath != wantExecPath {
		t.Fatalf("execPath = %q, want %q", execPath, wantExecPath)
	}
	if !reflect.DeepEqual(args, []string{"-c", "echo hi"}) {
		t.Fatalf("args = %#v, want passthrough args", args)
	}
	if hostCwd != root {
		t.Fatalf("hostCwd = %q, want %q", hostCwd, root)
	}
}

func TestWrapCommand_darwin_ignoresNetworkPolicy(t *testing.T) {
	root := "/tmp/test-workspace"
	allowAll := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
	}
	disabled := sandboxpkg.Policy{
		Filesystem: allowAll.Filesystem,
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkDisabled},
	}

	_, allowArgs, _, err := wrapCommand(allowAll, root, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("allow_all wrapCommand error: %v", err)
	}
	_, disabledArgs, _, err := wrapCommand(disabled, root, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("disabled wrapCommand error: %v", err)
	}

	if !reflect.DeepEqual(allowArgs, disabledArgs) {
		t.Fatalf("network policy should not affect darwin local wrapping: allow=%#v disabled=%#v", allowArgs, disabledArgs)
	}
}
