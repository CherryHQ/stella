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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

const seatbeltExecPath = "/usr/bin/sandbox-exec"

var (
	seatbeltOnce      sync.Once
	seatbeltAvailable bool
)

// seatbeltFunctional returns true when sandbox-exec is present and can run.
// Result is cached after the first call.
func seatbeltFunctional() bool {
	seatbeltOnce.Do(func() {
		if _, err := exec.LookPath(seatbeltExecPath); err != nil {
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
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	real := policy.WorkspaceRootOrDefault()
	return real, real
}

// createSessionTmpMounts returns tmpMount pairs for macOS temp paths.
// On macOS, /tmp and /var/tmp are symlinks into /private; sandbox-exec cannot
// bind-mount, so this maps file-tool access to the policy temp dir while shell
// commands still run on the host filesystem.
//
// To add support for a new sandbox temp path:
//  1. Resolve its symlink target and append a tmpMount{sandboxPath, realPath, owned}.
//  2. Add a corresponding allow rule in buildSeatbeltProfile if writes are needed.
func createSessionTmpMounts(policy sandboxpkg.Policy) ([]tmpMount, error) {
	resolve := func(p, fallback string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return fallback
	}
	tmp := policy.Filesystem.TempDirHost
	if tmp == "" {
		tmp = resolve("/tmp", "/private/tmp")
	} else if err := sandboxpkg.EnsurePrivateDir(tmp); err != nil {
		return nil, err
	}
	return []tmpMount{
		{sandboxPath: "/tmp", realPath: tmp},
		{sandboxPath: "/var/tmp", realPath: resolve("/var/tmp", "/private/var/tmp")},
	}, nil
}

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
func buildSeatbeltProfile(policy sandboxpkg.Policy) string {
	workspace := policy.WorkspaceRootOrDefault()
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

	// Workspace root: the only user-controlled path that is fully writable.
	fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", workspace)

	// Network: deny unless the policy explicitly requests unrestricted access.
	if networkMode != sandboxpkg.NetworkAllowAll {
		sb.WriteString("(deny network*)\n")
	}

	return sb.String()
}

// wrapCommand wraps name+args with sandbox-exec for macOS Seatbelt isolation.
// tmpMounts is accepted for signature compatibility with the Linux backend but
// is not used here — macOS bash and file tools share the same host filesystem.
func wrapCommand(policy sandboxpkg.Policy, sandboxCwd string, _ []tmpMount, name string, args []string) (execPath string, execArgs []string, hostCwd string, err error) {
	if !seatbeltFunctional() {
		return "", nil, "", fmt.Errorf(
			"local sandbox: sandbox-exec (macOS Seatbelt) is required but not available",
		)
	}

	resolved, lookErr := exec.LookPath(name)
	if lookErr != nil {
		return "", nil, "", fmt.Errorf("local exec: look up %q: %w", name, lookErr)
	}

	profile := buildSeatbeltProfile(policy)
	seatbeltArgs := []string{"-p", profile, resolved}
	seatbeltArgs = append(seatbeltArgs, args...)
	return seatbeltExecPath, seatbeltArgs, sandboxCwd, nil
}
