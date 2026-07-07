//go:build linux

package local

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
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
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	return sandboxpkg.MountWorkspace, policy.WorkspaceRootOrDefault()
}

// resolveUserDataRoot returns the sandbox-space and host paths of the shared
// user-data root. On Linux it is bind-mounted at the fixed path /user; "" host
// path (a user-less job) yields no second root.
func resolveUserDataRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	realRoot = policy.Filesystem.UserDataDir
	if realRoot == "" {
		return "", ""
	}
	return sandboxpkg.MountUserData, realRoot
}

// createSessionTmpMounts returns host directories for each sandbox temp path.
// /tmp uses the policy's user-scoped host directory when present; /var/tmp stays
// session-local. Owned directories are removed when the session closes.
//
// To add support for a new sandbox temp path (e.g. /run/user/1000):
//  1. Create or resolve the host dir and append a tmpMount{sandboxPath, realPath, owned}.
//  2. Add a corresponding "--bind realPath sandboxPath" entry in wrapCommand below.
//  3. Add the sandbox path to the platform profile in session_darwin.go if needed.
func createSessionTmpMounts(policy sandboxpkg.Policy) ([]tmpMount, error) {
	tmp := policy.Filesystem.TempDirHost
	tmpOwned := false
	if tmp == "" {
		var err error
		tmp, err = os.MkdirTemp("", "stella-session-tmp-*")
		if err != nil {
			return nil, err
		}
		tmpOwned = true
	}

	varTmp, err := os.MkdirTemp("", "stella-session-vartmp-*")
	if err != nil {
		if tmpOwned {
			os.RemoveAll(tmp) //nolint:errcheck
		}
		return nil, err
	}
	return []tmpMount{
		{sandboxPath: "/tmp", realPath: tmp, owned: tmpOwned},
		{sandboxPath: "/var/tmp", realPath: varTmp, owned: true},
	}, nil
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

func appendStellaHomeMounts(args []string, stellaHome string) []string {
	if stellaHome == "" {
		return args
	}
	for _, name := range sandboxpkg.StellaHomeSandboxDirs() {
		hostPath := filepath.Join(stellaHome, name)
		sandboxPath := filepath.Join(sandboxStellaHome, name)
		args = appendRoBindIfExists(args, hostPath, sandboxPath)
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

// appendWritableBind binds a host directory read-write into the sandbox,
// creating the sandbox-side parent directories first. Used for the per-user
// mise tree, the one STELLA_HOME subtree an agent may write to.
func appendWritableBind(args []string, hostPath, sandboxPath string) []string {
	args = appendDirParents(args, sandboxPath)
	args = append(args, "--dir", filepath.Clean(sandboxPath))
	return append(args, "--bind", hostPath, sandboxPath)
}

// remapToSandboxStellaHome rewrites a host path under STELLA_HOME to its
// sandbox-view location (STELLA_HOME is remapped to /opt/stella).
// Paths outside STELLA_HOME are returned unchanged.
func remapToSandboxStellaHome(hostPath, stellaHomeHost string) string {
	return remapStellaHomePath(hostPath, stellaHomeHost, sandboxStellaHome)
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
func wrapCommand(policy sandboxpkg.Policy, sandboxCwd string, tmpMounts []tmpMount, stellaHomeHost string, name string, args []string) (execPath string, execArgs []string, hostCwd string, err error) {
	if !bwrapFunctional() {
		return "", nil, "", fmt.Errorf(
			"local sandbox: bwrap (bubblewrap) is required on Linux but is not available or not functional; " +
				"install it (apt install bubblewrap / dnf install bubblewrap / pacman -S bubblewrap) " +
				"or use the docker backend",
		)
	}

	realRoot := policy.WorkspaceRootOrDefault()
	_, userDataReal := resolveUserDataRoot(policy)
	networkMode := policy.NetworkModeOrDefault()

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
	bwrapArgs = appendStellaHomeMounts(bwrapArgs, stellaHomeHost)
	// Agent-bound (system_agent) skills: read-only at a fixed path, so admin-managed
	// skills bound to this agent stay loadable without leaking the host path.
	if as := policy.Filesystem.AgentSkillsDir; as != "" {
		bwrapArgs = appendRoBindIfExists(bwrapArgs, as, sandboxpkg.MountAgentSkills)
	}
	// DB-installed system skills: read-only at a fixed path, kept separate from the
	// shipped built-ins so a system skill installed via Settings stays loadable.
	if sd := policy.Filesystem.SystemDBSkillsDir; sd != "" {
		bwrapArgs = appendRoBindIfExists(bwrapArgs, sd, sandboxpkg.MountSystemDBSkills)
	}
	// Extra writable mounts (e.g. an out-of-workspace per-principal cache), layered
	// above the read-only system installs mounted by appendStellaHomeMounts: bind
	// each at its STELLA_HOME-remapped sandbox path so writes land in the host tree.
	// A mount inside the workspace is skipped — it is already writable through the
	// realRoot -> /workspace bind below, and binding it again under the STELLA_HOME
	// tree would only re-expose the host path to the agent (#442).
	for _, writable := range policy.Filesystem.ExtraWritableMounts {
		// Already writable through a top-level bind — skip the separate
		// STELLA_HOME-style bind, which would re-expose the host path to the agent.
		if remapToSandboxRoot(writable, realRoot, "/workspace") != writable {
			continue // under /workspace
		}
		if userDataReal != "" && remapToSandboxRoot(writable, userDataReal, "/user") != writable {
			continue // under /user (an extra writable mount inside the user-data root)
		}
		bwrapArgs = appendWritableBind(bwrapArgs, writable, remapToSandboxStellaHome(writable, stellaHomeHost))
	}
	for _, extraPath := range policy.Filesystem.ExtraReadOnlyMounts {
		bwrapArgs = appendRoBindIfExists(bwrapArgs, extraPath, extraPath)
	}
	bwrapArgs = append(bwrapArgs,
		"--dir", "/workspace",
		"--bind", realRoot, "/workspace",
		"--chdir", sandboxCwd,
	)
	// Shared user-data root: bind the host UserDataDir writable at /user. Isolation
	// is by non-mounting — realRoot is the per-agent dir, so sibling agents are
	// never exposed and no tmpfs cover-up is needed (the #442 sibling-hiding hack
	// is gone). /user is a deliberate shared writable root for all of the user's
	// agents (caches, toolchain, uploads), not an isolation boundary.
	if userDataReal != "" {
		bwrapArgs = appendWritableBind(bwrapArgs, userDataReal, "/user")
	}
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
