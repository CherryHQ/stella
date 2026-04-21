//go:build linux

package local

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// setSysProcAttr places the child in its own process group so that
// killProcessGroup can terminate the entire subtree.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killProcessGroup sends SIGKILL to the process group of the given command.
// A negative PID targets the entire process group.
// No-ops when the process has already been reaped (ProcessState != nil).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.ProcessState == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// applyRlimits uses prlimit(2) to enforce resource limits on an already-started
// process. This avoids the need for RLIMIT_NPROC (which is per-UID and would
// affect the parent server process).
func applyRlimits(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid

	limits := []struct {
		resource int
		limit    unix.Rlimit
	}{
		{
			unix.RLIMIT_FSIZE,
			unix.Rlimit{Cur: 512 * 1024 * 1024, Max: 512 * 1024 * 1024}, // 512 MB
		},
		{
			unix.RLIMIT_NOFILE,
			unix.Rlimit{Cur: 1024, Max: 1024},
		},
		{
			unix.RLIMIT_CPU,
			unix.Rlimit{Cur: 300, Max: 300}, // 300 CPU seconds
		},
	}

	for _, l := range limits {
		lim := l.limit
		if err := unix.Prlimit(pid, l.resource, &lim, nil); err != nil {
			return fmt.Errorf("local: prlimit(pid=%d, resource=%d): %w", pid, l.resource, err)
		}
	}
	return nil
}

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On Linux, if bwrap is available, the agent always sees /workspace; otherwise
// both roots are identical.
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	real := policy.WorkspaceRootOrDefault()
	if _, err := exec.LookPath("bwrap"); err == nil {
		return "/workspace", real
	}
	return real, real
}

// wrapCommand wraps name+args with bwrap (preferred) or unshare for network/fs
// isolation on Linux.
//
//   - sandboxCwd: the working directory in sandbox space (e.g. /workspace/sub).
//   - hostCwd returned: what to set as cmd.Dir on the host (irrelevant for bwrap
//     since bwrap uses --chdir internally, but returned for consistency).
//
// If neither bwrap nor unshare is available and network isolation is required,
// an error is returned.
func wrapCommand(policy sandboxpkg.Policy, sandboxCwd, name string, args []string) (execPath string, execArgs []string, hostCwd string, err error) {
	realRoot := policy.WorkspaceRootOrDefault()
	networkMode := policy.NetworkModeOrDefault()

	// Always try bwrap first: it gives us both /workspace path remapping and
	// optional network isolation.
	bwrapPath, bwrapErr := exec.LookPath("bwrap")
	if bwrapErr == nil {
		bwrapArgs := []string{
			"--ro-bind", "/", "/",
			"--dir", "/workspace",
			"--bind", realRoot, "/workspace",
			"--dev", "/dev",
			"--tmpfs", "/tmp",
			"--proc", "/proc",
			"--chdir", sandboxCwd,
		}
		if networkMode != sandboxpkg.NetworkAllowAll {
			bwrapArgs = append(bwrapArgs, "--unshare-net")
		}
		bwrapArgs = append(bwrapArgs, "--", name)
		bwrapArgs = append(bwrapArgs, args...)
		// hostCwd is irrelevant for bwrap (--chdir handles it inside the sandbox).
		return bwrapPath, bwrapArgs, realRoot, nil
	}

	// No bwrap — fall through with real paths. sandboxRoot == realRoot here.
	if networkMode != sandboxpkg.NetworkAllowAll {
		unsharePath, unshareErr := exec.LookPath("unshare")
		if unshareErr == nil {
			unshareArgs := []string{"--net", "--", name}
			unshareArgs = append(unshareArgs, args...)
			return unsharePath, unshareArgs, sandboxCwd, nil
		}
		return "", nil, "", fmt.Errorf(
			"local sandbox: neither bwrap nor unshare is available; " +
				"network isolation cannot be enforced — " +
				"install bubblewrap or use the docker backend",
		)
	}
	return name, args, sandboxCwd, nil
}
