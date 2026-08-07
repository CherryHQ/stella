//go:build linux

package local

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

// sandboxStellaHome is the virtual STELLA_HOME inside the bwrap sandbox: the
// shared read-only system tree (toolchains, bin) the agent sees. It is deliberately
// an infrastructure path, not a home-shaped one — the agent's home is the user
// workspace at /workspace, set in adjustPolicy, so XDG defaults and the agent's own
// mise tree land under that home, never under this system tree (#442).
const sandboxStellaHome = sandboxpkg.MountStellaHome

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
func resolveSandboxRoot(layout hostlayout.Layout) (sandboxRoot, realRoot string) {
	return sandboxpkg.MountWorkspace, layout.WorkspaceSource
}

// resolveUserDataRoot returns the sandbox-space and host paths of the shared
// user-data root. On Linux it is bind-mounted at the fixed path /user; "" host
// path (a user-less job) yields no second root.
func resolveUserDataRoot(layout hostlayout.Layout) (sandboxRoot, realRoot string) {
	for _, mount := range layout.Mounts {
		if path.Clean(mount.Target) == sandboxpkg.MountUserData {
			return sandboxpkg.MountUserData, mount.Source
		}
	}
	return "", ""
}

// createSessionTmpMounts returns session-private host directories for each
// sandbox temp path. Owned directories are removed when the session closes.
//
// To add support for a new sandbox temp path (e.g. /run/user/1000):
//  1. Create or resolve the host dir and append a tmpMount{sandboxPath, realPath, owned}.
//  2. Add a corresponding "--bind realPath sandboxPath" entry in wrapCommand below.
//  3. Add the sandbox path to the platform profile in session_darwin.go if needed.
func createSessionTmpMounts() ([]tmpMount, error) {
	tmp, err := os.MkdirTemp("", "stella-session-tmp-*")
	if err != nil {
		return nil, err
	}
	varTmp, err := os.MkdirTemp("", "stella-session-vartmp-*")
	if err != nil {
		os.RemoveAll(tmp) //nolint:errcheck
		return nil, err
	}
	return []tmpMount{
		{sandboxPath: "/tmp", realPath: tmp, owned: true},
		{sandboxPath: "/var/tmp", realPath: varTmp, owned: true},
	}, nil
}

// filesystemTempDir returns the Linux bwrap process view. /tmp is always bind
// mounted, regardless of the backing host directory.
func filesystemTempDir(_ []tmpMount) string { return "/tmp" }

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
		// Probe with a minimal sandbox that just runs true(1), including the
		// namespace isolation flags used by real execs.
		args := []string{
			"--unshare-pid", "--unshare-ipc", "--unshare-uts",
			"--dev-bind", "/", "/", "--", "true",
		}
		if exec.Command(p, args...).Run() == nil {
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

// adjustStellaHome returns the sandbox-view STELLA_HOME directory.
// On Linux (bwrap), it is remapped to /opt/stella.
func adjustStellaHome(_ string) string { return sandboxStellaHome }

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

// appendWritableBind binds a host directory read-write into the sandbox,
// creating the sandbox-side parent directories first. Used for the per-user
// mise tree, the one STELLA_HOME subtree an agent may write to.
func appendWritableBind(args []string, hostPath, sandboxPath string) []string {
	args = appendDirParents(args, sandboxPath)
	args = append(args, "--dir", filepath.Clean(sandboxPath))
	return append(args, "--bind", hostPath, sandboxPath)
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

func isCoveredByWritableMount(hostPath string, mounts []sandboxpkg.Mount) bool {
	for _, m := range mounts {
		if m.Access != sandboxpkg.MountReadWrite || filepath.Clean(m.HostPath) == filepath.Clean(hostPath) {
			continue
		}
		rel, err := filepath.Rel(m.HostPath, hostPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// wrapCommand wraps name+args with bwrap for filesystem and optional network
// isolation on Linux. bwrap is mandatory — returns an error if not functional.
//
//   - sandboxCwd: working directory in sandbox space (e.g. /workspace/sub).
func wrapCommand(policy sandboxpkg.Policy, layout hostlayout.Layout, sandboxCwd string, tmpMounts []tmpMount, _ string, name string, args []string) (execPath string, execArgs []string, err error) {
	if !bwrapFunctional() {
		return "", nil, fmt.Errorf(
			"local sandbox: bwrap (bubblewrap) is required on Linux but is not available or not functional; " +
				"install it (apt install bubblewrap / dnf install bubblewrap / pacman -S bubblewrap) " +
				"or use the docker backend",
		)
	}

	realRoot := layout.WorkspaceSource
	networkMode := policy.NetworkModeOrDefault()
	mounts := policyMounts(layout)

	bwrapArgs := []string{
		"--die-with-parent",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--tmpfs", "/",
		"--dev", "/dev",
		"--tmpfs", "/dev/shm",
		"--dir", "/var",
		"--tmpfs", "/var/tmp",
		"--proc", "/proc",
		"--dir", "/run",
	}
	// User namespace remapping and seccomp are deferred until they have real Linux coverage.
	// Mount temp directories: bind each per-session host dir so file tools on
	// the host share the same view as bash running inside bwrap.
	// See createSessionTmpMounts for how to add a new temp path.
	for _, m := range tmpMounts {
		bwrapArgs = append(bwrapArgs, "--dir", m.sandboxPath, "--bind", m.realPath, m.sandboxPath)
	}
	bwrapArgs = appendLinuxRuntimeMounts(bwrapArgs)
	for _, m := range mounts {
		if m.SandboxPath == sandboxpkg.MountWorkspace || m.SandboxPath == sandboxpkg.MountUserData {
			continue
		}
		if m.Access == sandboxpkg.MountReadWrite && isCoveredByWritableMount(m.HostPath, mounts) {
			continue
		}
		if m.Access == sandboxpkg.MountReadOnly {
			bwrapArgs = appendRoBindIfExists(bwrapArgs, m.HostPath, m.SandboxPath)
			continue
		}
		bwrapArgs = appendWritableBind(bwrapArgs, m.HostPath, m.SandboxPath)
	}
	workspaceMount, _ := mountBySandboxPath(mounts, sandboxpkg.MountWorkspace)
	if workspaceMount.HostPath == "" {
		workspaceMount = sandboxpkg.Mount{HostPath: realRoot, SandboxPath: sandboxpkg.MountWorkspace, Access: sandboxpkg.MountReadWrite}
	}
	bwrapArgs = append(bwrapArgs,
		"--dir", workspaceMount.SandboxPath,
		"--bind", workspaceMount.HostPath, workspaceMount.SandboxPath,
		"--chdir", sandboxCwd,
	)
	if userMount, ok := mountBySandboxPath(mounts, sandboxpkg.MountUserData); ok {
		bwrapArgs = appendWritableBind(bwrapArgs, userMount.HostPath, userMount.SandboxPath)
	}
	// Re-mount workspace-contained read-only paths at their /workspace/... equivalent.
	// This overrides the writable workspace bind for those subdirectories.
	for _, m := range mounts {
		if m.Access != sandboxpkg.MountReadOnly {
			continue
		}
		rel, relErr := filepath.Rel(workspaceMount.HostPath, filepath.Clean(m.HostPath))
		if relErr != nil || strings.HasPrefix(rel, "..") || rel == "." {
			continue
		}
		bwrapArgs = appendRoBindIfExists(bwrapArgs, m.HostPath, filepath.Join(workspaceMount.SandboxPath, rel))
	}
	if networkMode != sandboxpkg.NetworkAllowAll {
		bwrapArgs = append(bwrapArgs, "--unshare-net")
	}
	bwrapArgs = append(bwrapArgs, "--", name)
	bwrapArgs = append(bwrapArgs, args...)
	return bwrapPath, bwrapArgs, nil
}
