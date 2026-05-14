//go:build linux

package local

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
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

// checkSandboxRequirements verifies that bwrap is available and functional.
// Returns an error with install instructions if not.
func checkSandboxRequirements() error {
	if !bwrapFunctional() {
		return fmt.Errorf(
			"local sandbox: bwrap (bubblewrap) is required on Linux but is not available or not functional; " +
				"install it (apt install bubblewrap / dnf install bubblewrap / pacman -S bubblewrap) " +
				"or use the docker backend",
		)
	}
	return nil
}

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On Linux bwrap is required, so the agent always sees /workspace.
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	return "/workspace", policy.WorkspaceRootOrDefault()
}

var (
	bwrapOnce      sync.Once
	bwrapPath      string
	bwrapAvailable bool
)

// bwrapFunctional returns true only when bwrap is on PATH AND can actually
// create a user namespace. The result is cached after the first call.
// Inside Docker containers without --privileged, bwrap is often installed
// but namespace creation is blocked by the seccomp profile, causing
// "Operation not permitted" at runtime.
func bwrapFunctional() bool {
	bwrapOnce.Do(func() {
		p, err := exec.LookPath("bwrap")
		if err != nil {
			return
		}
		// Probe with a minimal sandbox that just runs true(1).
		if exec.Command(p, "--dev-bind", "/", "/", "--", "true").Run() == nil {
			bwrapPath = p
			bwrapAvailable = true
		}
	})
	return bwrapAvailable
}

func appendLinuxRuntimeMounts(args []string) []string {
	// Keep the local sandbox root small: runtime/tooling directories are mounted
	// read-only, while user-writable state is limited to /workspace and /tmp.
	for _, path := range []string{
		"/usr",
		"/bin",
		"/sbin",
		"/lib",
		"/lib64",
		"/lib32",
		"/etc",
		"/nix",
		"/run/current-system/sw",
		"/run/systemd/resolve",
		"/run/resolvconf",
		"/run/NetworkManager",
	} {
		args = appendRoBindIfExists(args, path, path)
	}
	args = appendResolvedFileMount(args, "/etc/resolv.conf")
	// Expose system device topology (CPU, clocksource, etc.) so tools like
	// onnxruntime/cpuinfo can detect hardware features. Only this subtree is
	// mounted; the rest of /sys stays hidden.
	args = appendRoBindIfExists(args, "/sys/devices/system", "/sys/devices/system")
	return args
}

func appendStellaHomeMounts(args []string, stellaHome string) []string {
	if stellaHome == "" {
		return args
	}
	for _, name := range []string{"bin", filepath.Join(".agents", "skills")} {
		hostPath := filepath.Join(stellaHome, name)
		args = appendRoBindIfExists(args, hostPath, hostPath)
	}
	return args
}

func appendResolvedFileMount(args []string, path string) []string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved == path {
		return args
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return args
	}
	return appendRoBindIfExists(args, resolved, resolved)
}

func appendRoBindIfExists(args []string, hostPath, sandboxPath string) []string {
	info, err := os.Stat(hostPath)
	if err != nil {
		return args
	}
	args = appendDirParents(args, sandboxPath)
	if info.IsDir() {
		args = append(args, "--dir", filepath.Clean(sandboxPath))
	}
	return append(args, "--ro-bind", hostPath, sandboxPath)
}

func appendDirParents(args []string, path string) []string {
	parent := filepath.Clean(filepath.Dir(path))
	if parent == "." || parent == string(filepath.Separator) {
		return args
	}
	var dirs []string
	for parent != "." && parent != string(filepath.Separator) {
		dirs = append(dirs, parent)
		parent = filepath.Dir(parent)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		args = append(args, "--dir", dirs[i])
	}
	return args
}

// wrapCommand wraps name+args with bwrap for filesystem and optional network
// isolation on Linux. bwrap is mandatory — returns an error if not functional.
//
//   - sandboxCwd: working directory in sandbox space (e.g. /workspace/sub).
//   - hostCwd returned: real host path (bwrap uses --chdir internally).
func wrapCommand(policy sandboxpkg.Policy, sandboxCwd, name string, args []string) (execPath string, execArgs []string, hostCwd string, err error) {
	if !bwrapFunctional() {
		return "", nil, "", fmt.Errorf(
			"local sandbox: bwrap (bubblewrap) is required on Linux but is not available or not functional; " +
				"install it (apt install bubblewrap / dnf install bubblewrap / pacman -S bubblewrap) " +
				"or use the docker backend",
		)
	}

	realRoot := policy.WorkspaceRootOrDefault()
	networkMode := policy.NetworkModeOrDefault()

	bwrapArgs := []string{
		"--tmpfs", "/",
		"--dev", "/dev",
		"--tmpfs", "/dev/shm",
		"--tmpfs", "/tmp",
		"--dir", "/var",
		"--tmpfs", "/var/tmp",
		"--proc", "/proc",
		"--dir", "/run",
	}
	bwrapArgs = appendLinuxRuntimeMounts(bwrapArgs)
	bwrapArgs = appendStellaHomeMounts(bwrapArgs, policy.Env["STELLA_HOME"])
	for _, extraPath := range policy.Filesystem.ExtraReadOnlyMounts {
		bwrapArgs = appendRoBindIfExists(bwrapArgs, extraPath, extraPath)
	}
	bwrapArgs = append(bwrapArgs,
		"--dir", "/workspace",
		"--bind", realRoot, "/workspace",
		"--chdir", sandboxCwd,
	)
	// Re-mount workspace-contained extra paths read-only at their /workspace/...
	// equivalent. This overrides the writable workspace bind for those subdirectories
	// so bash cannot write to them via /workspace.
	for _, extraPath := range policy.Filesystem.ExtraReadOnlyMounts {
		rel, relErr := filepath.Rel(realRoot, filepath.Clean(extraPath))
		if relErr != nil || strings.HasPrefix(rel, "..") || rel == "." {
			continue
		}
		bwrapArgs = appendRoBindIfExists(bwrapArgs, extraPath, filepath.Join("/workspace", rel))
	}
	if networkMode != sandboxpkg.NetworkAllowAll {
		bwrapArgs = append(bwrapArgs, "--unshare-net")
	}
	bwrapArgs = append(bwrapArgs, "--", name)
	bwrapArgs = append(bwrapArgs, args...)
	return bwrapPath, bwrapArgs, realRoot, nil
}
