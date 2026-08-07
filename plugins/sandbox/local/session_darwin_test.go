package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

func TestFilesystemTempDirDarwinUsesRealTmpMount(t *testing.T) {
	if got, want := filesystemTempDir([]tmpMount{{sandboxPath: "/tmp", realPath: "/private/var/folders/principal-tmp"}}), "/private/var/folders/principal-tmp"; got != want {
		t.Errorf("filesystemTempDir = %q, want %q", got, want)
	}
	if got, want := filesystemTempDir(nil), os.TempDir(); got != want {
		t.Errorf("filesystemTempDir fallback = %q, want %q", got, want)
	}
}

func makePolicy(root string, net sandboxpkg.NetworkMode) sandboxpkg.Policy {
	return sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: root,
		},
		Network: sandboxpkg.NetworkPolicy{Mode: net},
	}
}

func TestWrapCommand_darwin_usesSeatbelt(t *testing.T) {
	if !seatbeltBinaryAvailable() {
		t.Skip("sandbox-exec not available")
	}
	root := t.TempDir()
	policy := makePolicy(root, sandboxpkg.NetworkDisabled)

	execPath, args, err := wrapCommand(policy, layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir), root, nil, "", "sh", []string{"-c", "echo hi"})
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
}

// TestLocalSession_darwinProductionMountUsesHostCwd verifies that direct
// Seatbelt execution starts in the host path for a production-style /workspace
// mount. sandbox-exec does not remap paths, unlike bwrap.
func TestLocalSession_darwinProductionMountUsesHostCwd(t *testing.T) {
	if !seatbeltBinaryAvailable() {
		t.Skip("sandbox-exec not available")
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	realCwd := filepath.Join(root, "sub")
	if err := os.Mkdir(realCwd, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	sandboxCwd := filepath.Join(sandboxpkg.MountWorkspace, "sub")
	policy := sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkingDir: sandboxpkg.MountWorkspace,
		},
		Network:    sandboxpkg.NetworkPolicy{Mode: sandboxpkg.NetworkAllowAll},
		InheritEnv: true,
	}
	s := &localSession{
		id:          "test",
		policy:      policy,
		layout:      layoutFor(root, root, hostlayout.Mount{Source: root, Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite}),
		realRoot:    root,
		sandboxRoot: sandboxpkg.MountWorkspace,
		done:        make(chan struct{}),
	}
	want := realCwd + "\n"

	t.Run("Exec", func(t *testing.T) {
		result, err := s.Exec(context.Background(), "pwd", sandboxpkg.ExecOptions{Cwd: sandboxCwd})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("Exec exit code = %d, stderr = %q", result.ExitCode, result.Stderr)
		}
		if result.Stdout != want {
			t.Errorf("Exec stdout = %q, want %q", result.Stdout, want)
		}
	})

	t.Run("StartProcess", func(t *testing.T) {
		process, err := s.StartProcess(context.Background(), sandboxpkg.ProcessRequest{
			Path: "/bin/pwd",
			Cwd:  sandboxCwd,
		})
		if err != nil {
			t.Fatalf("StartProcess: %v", err)
		}
		stdout, err := io.ReadAll(process.Stdout())
		if err != nil {
			t.Fatalf("read stdout: %v", err)
		}
		result, err := process.Wait(context.Background())
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("StartProcess exit code = %d", result.ExitCode)
		}
		if string(stdout) != want {
			t.Errorf("StartProcess stdout = %q, want %q", stdout, want)
		}
	})
}

func TestBuildSeatbeltProfile_structure(t *testing.T) {
	policy := makePolicy("/tmp/ws", sandboxpkg.NetworkDisabled)
	profile := buildSeatbeltProfile(policy, layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir), "")

	for _, want := range []string{
		"(allow default)",
		`(deny file-write* (subpath "/"))`,
		`(allow file-write* (subpath "/private/tmp"))`,
		`(allow file-write* (subpath "/private/var/folders"))`,
		`(allow file-write* (subpath "/private/var/tmp"))`,
		`(allow file-write* (subpath "/dev"))`,
		`(allow file-write* (subpath "/tmp/ws"))`,
		"(deny network*)",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing: %s", want)
		}
	}
}

func TestBuildSeatbeltProfile_allowsMiseRuntimeWriteDirs(t *testing.T) {
	stellaHome := t.TempDir()
	policy := makePolicy("/tmp/ws", sandboxpkg.NetworkDisabled)
	policy.Env = map[string]string{
		"MISE_CACHE_DIR": filepath.Join(stellaHome, ".mise-tools", "cache"),
		"MISE_STATE_DIR": "/tmp/mise-state",
	}
	profile := buildSeatbeltProfile(policy, layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir), "")

	for _, want := range []string{
		`(allow file-write* (subpath "` + filepath.Join(stellaHome, ".mise-tools", "cache") + `"))`,
		`(allow file-write* (subpath "/tmp/mise-state"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing: %s", want)
		}
	}
}

func TestBuildSeatbeltProfile_ignoresUnsafeMiseRuntimeWriteDirs(t *testing.T) {
	policy := makePolicy("/tmp/ws", sandboxpkg.NetworkDisabled)
	policy.Env = map[string]string{
		"MISE_CACHE_DIR": "/",
		"MISE_STATE_DIR": "relative/state",
	}
	profile := buildSeatbeltProfile(policy, layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir), "")

	for _, forbidden := range []string{
		`(allow file-write* (subpath "/"))`,
		`(allow file-write* (subpath "relative/state"))`,
	} {
		if strings.Contains(profile, forbidden) {
			t.Errorf("profile must not contain unsafe allow: %s", forbidden)
		}
	}
}

// TestBuildSeatbeltProfile_miseHolesKeyedOnPerUserTree verifies the cache/state
// fallback holes are keyed on the per-user mise tree recovered from the env, not
// on len(ExtraWritableMounts): an unrelated writable mount must not suppress them
// (the #442 scenario), and a real per-user mise tree must.
func TestBuildSeatbeltProfile_miseHolesKeyedOnPerUserTree(t *testing.T) {
	stellaHome := t.TempDir()
	cacheDir := filepath.Join(stellaHome, ".mise-tools", "cache")

	t.Run("unrelated writable mount still emits cache holes", func(t *testing.T) {
		policy := makePolicy("/tmp/ws", sandboxpkg.NetworkDisabled)
		policy.Env = map[string]string{"MISE_CACHE_DIR": cacheDir}
		layout := layoutFor("/tmp/ws", "/tmp/ws", hostlayout.Mount{Source: "/tmp/ws", Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite}, hostlayout.Mount{Source: "/tmp/unrelated", Target: "/tmp/unrelated", Access: hostlayout.ReadWrite})
		profile := buildSeatbeltProfile(policy, layout, stellaHome)

		if !strings.Contains(profile, `(allow file-write* (subpath "`+cacheDir+`"))`) {
			t.Errorf("an unrelated writable mount must not suppress mise cache holes:\n%s", profile)
		}
	})

	t.Run("per-user mise tree skips cache fallback", func(t *testing.T) {
		userDir := filepath.Join(stellaHome, "users", "u1", ".mise-tools")
		policy := makePolicy("/tmp/ws", sandboxpkg.NetworkDisabled)
		policy.Env = map[string]string{
			"MISE_DATA_DIR":  userDir,
			"MISE_CACHE_DIR": "/tmp/mise-cache",
		}
		layout := layoutFor("/tmp/ws", "/tmp/ws", hostlayout.Mount{Source: "/tmp/ws", Target: sandboxpkg.MountWorkspace, Access: hostlayout.ReadWrite}, hostlayout.Mount{Source: userDir, Target: userDir, Access: hostlayout.ReadWrite})
		profile := buildSeatbeltProfile(policy, layout, stellaHome)

		if strings.Contains(profile, `(allow file-write* (subpath "/tmp/mise-cache"))`) {
			t.Errorf("a writable per-user mise tree should skip the cache fallback holes:\n%s", profile)
		}
	})
}

// TestBuildSeatbeltProfile_noSiblingRules verifies generic mount policy emits no
// legacy per-agent deny/allow rules.
func TestBuildSeatbeltProfile_noSiblingRules(t *testing.T) {
	profile := buildSeatbeltProfile(makePolicy("/private/tmp/ws", sandboxpkg.NetworkDisabled), layoutFor("/private/tmp/ws", "/private/tmp/ws"), "")
	if strings.Contains(profile, "(deny file-read* file-write*") {
		t.Errorf("no sibling-hiding rules expected without generic mounts:\n%s", profile)
	}
}

func TestBuildSeatbeltProfile_networkAllowAll(t *testing.T) {
	policy := makePolicy("/tmp/ws", sandboxpkg.NetworkAllowAll)
	profile := buildSeatbeltProfile(policy, layoutFor(policy.Filesystem.WorkingDir, policy.Filesystem.WorkingDir), "")

	if strings.Contains(profile, "(deny network*)") {
		t.Error("profile must not deny network when mode is allow_all")
	}
}

func TestBuildSeatbeltProfile_networkDisabledVsAllowAll(t *testing.T) {
	if !seatbeltBinaryAvailable() {
		t.Skip("sandbox-exec not available")
	}
	root := t.TempDir()

	_, disabledArgs, err := wrapCommand(makePolicy(root, sandboxpkg.NetworkDisabled), layoutFor(root, root), root, nil, "", "sh", []string{"-c", "echo"})
	if err != nil {
		t.Fatalf("disabled wrapCommand error: %v", err)
	}
	_, allowArgs, err := wrapCommand(makePolicy(root, sandboxpkg.NetworkAllowAll), layoutFor(root, root), root, nil, "", "sh", []string{"-c", "echo"})
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
