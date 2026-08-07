//go:build darwin

// macOS Seatbelt (sandbox-exec) isolation for the local backend.
// sandbox-exec is deprecated since macOS 10.15 but still ships and works;
// if Apple ever removes it, checkSandboxRequirements will catch the absence
// and the factory will fall through to the next available backend.
//
// Profile strategy: start with (allow default) so that dynamic linking,
// mach IPC, and getcwd all work without enumeration, then deny writes to the
// whole filesystem and re-allow writes only to the workspace root, temp dirs,
// and /dev. Network access is denied unless the policy requests allow_all.
// SBPL uses last-match-wins semantics, so ordering matters.
package local

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

const seatbeltExecPath = "/usr/bin/sandbox-exec"

var (
	seatbeltOnce      sync.Once
	seatbeltAvailable bool
)

// seatbeltBinaryAvailable reports whether the exact executable path can be run.
// Do not use the functional probe to skip execution tests: a present but broken
// sandbox-exec is a production failure, not an unsupported test host.
func seatbeltBinaryAvailable() bool {
	info, err := os.Stat(seatbeltExecPath)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// seatbeltFunctional returns true when sandbox-exec is present and can run.
// Result is cached after the first call.
func seatbeltFunctional() bool {
	seatbeltOnce.Do(func() {
		if !seatbeltBinaryAvailable() {
			return
		}
		// Probe with an allow-all profile to verify the binary works.
		if exec.Command(seatbeltExecPath, "-p", "(version 1)(allow default)", "/usr/bin/true").Run() == nil {
			seatbeltAvailable = true
		}
	})
	return seatbeltAvailable
}

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On macOS there is no path remapping; both are always identical.
func resolveSandboxRoot(layout hostlayout.Layout) (sandboxRoot, realRoot string) {
	real := layout.WorkspaceSource
	return real, real
}

// resolveUserDataRoot returns the shared user-data root. macOS does no path
// remapping, so the sandbox-space and host paths are identical (no /user alias).
func resolveUserDataRoot(layout hostlayout.Layout) (sandboxRoot, realRoot string) {
	for _, mount := range layout.Mounts {
		if path.Clean(mount.Target) == sandboxpkg.MountUserData {
			return mount.Source, mount.Source
		}
	}
	return "", ""
}

// createSessionTmpMounts returns session-private host directories for the macOS
// temporary roots. Seatbelt has no bind mounts, so TMPDIR names the real backing
// directory and the profile grants access to that exact path.
func createSessionTmpMounts(layout hostlayout.Layout) ([]tmpMount, error) {
	tmp, err := hostlayout.CreateSessionTempDir(layout, "tmp-*")
	if err != nil {
		return nil, err
	}
	varTmp, err := hostlayout.CreateSessionTempDir(layout, "var-tmp-*")
	if err != nil {
		os.RemoveAll(tmp) //nolint:errcheck
		return nil, err
	}
	return []tmpMount{
		{sandboxPath: "/tmp", realPath: tmp, owned: true},
		{sandboxPath: "/var/tmp", realPath: varTmp, owned: true},
	}, nil
}

// filesystemTempDir returns the macOS process view: Seatbelt has no path
// remapping, so TMPDIR must name the real directory backing /tmp.
func filesystemTempDir(mounts []tmpMount) string {
	for _, mount := range mounts {
		if mount.sandboxPath == "/tmp" && mount.realPath != "" {
			return mount.realPath
		}
	}
	return os.TempDir()
}

// adjustStellaHome returns the sandbox-view STELLA_HOME directory.
// On macOS (Seatbelt), no path remapping; uses the real host path.
func adjustStellaHome(stellaHome string) string { return stellaHome }

// checkSandboxRequirements verifies that sandbox-exec is available and functional.
func checkSandboxRequirements() error {
	if !seatbeltFunctional() {
		return fmt.Errorf(
			"local sandbox: sandbox-exec (macOS Seatbelt) is required but not available; " +
				"this binary ships with macOS at /usr/bin/sandbox-exec",
		)
	}
	return nil
}

// buildSeatbeltProfile returns an SBPL profile string for the given policy.
//
// The profile uses (allow default) as the base so that system operations
// (dynamic linking, mach IPC, getcwd) work without enumerating every required
// path. All filesystem writes are then denied globally and re-allowed only for
// the workspace, temp directories, and device nodes. Network is denied when
// the policy requests NetworkDisabled.
//
// SBPL evaluates rules in order and the last match wins.
//
// stellaHomeHost is the host STELLA_HOME, used to recognize whether the policy
// carries a writable per-user mise tree (see the cache/state fallback below).
func buildSeatbeltProfile(policy sandboxpkg.Policy, layout hostlayout.Layout, stellaHomeHost string) string {
	workspace := layout.WorkspaceSource
	networkMode := policy.NetworkModeOrDefault()

	var sb strings.Builder
	sb.WriteString("(version 1)\n")

	// Base: allow everything so the toolchain and shell work out of the box.
	sb.WriteString("(allow default)\n")

	// Deny all filesystem writes. Re-allows below (last-match-wins) carve out
	// the locations that the sandbox legitimately needs to write to.
	sb.WriteString("(deny file-write* (subpath \"/\"))\n")

	// Temp directories: process-local scratch space, macOS per-user temp, and
	// persistent scratch (mirrors Linux /var/tmp).
	sb.WriteString("(allow file-write* (subpath \"/private/tmp\"))\n")
	sb.WriteString("(allow file-write* (subpath \"/private/var/folders\"))\n")
	sb.WriteString("(allow file-write* (subpath \"/private/var/tmp\"))\n")

	// Dev nodes: required for stdout/stderr, pseudo-terminals, /dev/null, etc.
	sb.WriteString("(allow file-write* (subpath \"/dev\"))\n")

	// Writable mounts (e.g. the per-user mise home): carve out each subtree so the
	// agent can write through it — for mise that's installs/cache/state.
	for _, mount := range layout.Mounts {
		if mount.Access == hostlayout.ReadWrite {
			fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", filepath.Clean(mount.Source))
		}
	}
	// With no writable per-user mise tree, the shared system installs stay
	// read-only; mise still needs its cache/state metadata holes open. Key off the
	// per-user mise data dir recovered from the env — not the mount count — so an
	// unrelated writable mount cannot suppress these holes. This mirrors how the
	// none/linux backends recover the per-user mise home from the env.
	if sandboxpkg.PerUserMiseDataDir(policy.Env, stellaHomeHost) == "" {
		appendSeatbeltWritableEnvDirs(&sb, policy.Env)
	}

	// Workspace root: the agent's fully writable working tree.
	fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", workspace)

	// Network: deny unless the policy explicitly requests unrestricted access.
	if networkMode != sandboxpkg.NetworkAllowAll {
		sb.WriteString("(deny network*)\n")
	}

	return sb.String()
}

func appendSeatbeltWritableEnvDirs(sb *strings.Builder, env map[string]string) {
	for _, key := range []string{"MISE_CACHE_DIR", "MISE_STATE_DIR"} {
		dir := filepath.Clean(env[key])
		if dir == "." || dir == string(filepath.Separator) || !filepath.IsAbs(dir) {
			continue
		}
		fmt.Fprintf(sb, "(allow file-write* (subpath %q))\n", dir)
	}
}

// wrapCommand wraps name+args with sandbox-exec for macOS Seatbelt isolation.
// tmpMounts is accepted for signature compatibility with the Linux backend but
// is not used here — macOS bash and file tools share the same host filesystem.
func wrapCommand(policy sandboxpkg.Policy, layout hostlayout.Layout, _ string, _ []tmpMount, stellaHomeHost string, name string, args []string) (execPath string, execArgs []string, err error) {
	if !seatbeltFunctional() {
		return "", nil, fmt.Errorf(
			"local sandbox: sandbox-exec (macOS Seatbelt) is required but not available",
		)
	}

	resolved, lookErr := exec.LookPath(name)
	if lookErr != nil {
		return "", nil, fmt.Errorf("local exec: look up %q: %w", name, lookErr)
	}

	profile := buildSeatbeltProfile(policy, layout, stellaHomeHost)
	seatbeltArgs := []string{"-p", profile, resolved}
	seatbeltArgs = append(seatbeltArgs, args...)
	return seatbeltExecPath, seatbeltArgs, nil
}
