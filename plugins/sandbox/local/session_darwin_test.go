package local

import (
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func makePolicy(root string, net sandboxpkg.NetworkMode) sandboxpkg.Policy {
	return sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: net},
	}
}

func TestWrapCommand_darwin_usesSeatbelt(t *testing.T) {
	if !seatbeltFunctional() {
		t.Skip("sandbox-exec not available")
	}
	root := t.TempDir()
	policy := makePolicy(root, sandboxpkg.NetworkDisabled)

	execPath, args, hostCwd, err := wrapCommand(policy, root, "sh", []string{"-c", "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if execPath != seatbeltExecPath {
		t.Fatalf("execPath = %q, want %q", execPath, seatbeltExecPath)
	}
	// args: ["-p", <profile>, <resolved-sh>, "-c", "echo hi"]
	if len(args) < 3 || args[0] != "-p" {
		t.Fatalf("expected -p <profile> <cmd> ..., got %v", args)
	}
	if hostCwd != root {
		t.Fatalf("hostCwd = %q, want %q", hostCwd, root)
	}
}

func TestBuildSeatbeltProfile_structure(t *testing.T) {
	policy := makePolicy("/tmp/ws", sandboxpkg.NetworkDisabled)
	profile := buildSeatbeltProfile(policy)

	for _, want := range []string{
		"(allow default)",
		`(deny file-write* (subpath "/"))`,
		`(allow file-write* (subpath "/private/tmp"))`,
		`(allow file-write* (subpath "/private/var/folders"))`,
		`(allow file-write* (subpath "/dev"))`,
		`(allow file-write* (subpath "/tmp/ws"))`,
		"(deny network*)",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing: %s", want)
		}
	}
}

func TestBuildSeatbeltProfile_networkAllowAll(t *testing.T) {
	policy := makePolicy("/tmp/ws", sandboxpkg.NetworkAllowAll)
	profile := buildSeatbeltProfile(policy)

	if strings.Contains(profile, "(deny network*)") {
		t.Error("profile must not deny network when mode is allow_all")
	}
}

func TestBuildSeatbeltProfile_networkDisabledVsAllowAll(t *testing.T) {
	if !seatbeltFunctional() {
		t.Skip("sandbox-exec not available")
	}
	root := t.TempDir()

	_, disabledArgs, _, err := wrapCommand(makePolicy(root, sandboxpkg.NetworkDisabled), root, "sh", []string{"-c", "echo"})
	if err != nil {
		t.Fatalf("disabled wrapCommand error: %v", err)
	}
	_, allowArgs, _, err := wrapCommand(makePolicy(root, sandboxpkg.NetworkAllowAll), root, "sh", []string{"-c", "echo"})
	if err != nil {
		t.Fatalf("allow_all wrapCommand error: %v", err)
	}

	disabledProfile := disabledArgs[1]
	allowProfile := allowArgs[1]
	if disabledProfile == allowProfile {
		t.Error("network policies should produce different profiles")
	}
	if !strings.Contains(disabledProfile, "(deny network*)") {
		t.Error("disabled profile must contain network deny")
	}
	if strings.Contains(allowProfile, "(deny network*)") {
		t.Error("allow_all profile must not contain network deny")
	}
}
