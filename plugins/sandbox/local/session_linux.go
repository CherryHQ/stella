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

// wrapCommand wraps name+args with bwrap (preferred) or unshare for network
// isolation on Linux. If neither tool is available and network isolation is
// required, an error is returned.
func wrapCommand(policy sandboxpkg.Policy, name string, args []string) (string, []string, error) {
	workspaceRoot := policy.WorkspaceRootOrDefault()
	networkMode := policy.NetworkModeOrDefault()

	// Finding 6: when network is allow_all, no isolation is needed — run unwrapped.
	if networkMode == sandboxpkg.NetworkAllowAll {
		return name, args, nil
	}

	// Network isolation required — try bubblewrap first.
	bwrapPath, err := exec.LookPath("bwrap")
	if err == nil {
		bwrapArgs := []string{
			"--ro-bind", "/", "/",
			"--bind", workspaceRoot, workspaceRoot,
			"--dev", "/dev",
			"--tmpfs", "/tmp",
			"--proc", "/proc",
			"--unshare-net",
		}
		bwrapArgs = append(bwrapArgs, "--")
		bwrapArgs = append(bwrapArgs, name)
		bwrapArgs = append(bwrapArgs, args...)
		return bwrapPath, bwrapArgs, nil
	}

	// Fall back to unshare for network-only isolation.
	unsharePath, err := exec.LookPath("unshare")
	if err == nil {
		unshareArgs := []string{"--net", "--"}
		unshareArgs = append(unshareArgs, name)
		unshareArgs = append(unshareArgs, args...)
		return unsharePath, unshareArgs, nil
	}

	// Neither tool found — network isolation cannot be enforced.
	return "", nil, fmt.Errorf(
		"local sandbox: neither bwrap nor unshare is available; " +
			"network isolation cannot be enforced — " +
			"install bubblewrap or use the docker backend",
	)
}
