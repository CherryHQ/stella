//go:build !linux && !darwin

package local

import (
	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On platforms other than Linux and macOS there is no path remapping.
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	real := policy.WorkspaceRootOrDefault()
	return real, real
}

// resolveUserDataRoot returns the shared user-data root. There is no path
// remapping on this platform, so the sandbox-space and host paths are identical.
func resolveUserDataRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	if m, ok := mountBySandboxPath(policy.Filesystem.Mounts, sandboxpkg.MountUserData); ok {
		return m.HostPath, m.HostPath
	}
	return "", ""
}

// createSessionTmpMounts returns no temp mounts on platforms other than Linux and macOS.
func createSessionTmpMounts(sandboxpkg.Policy) ([]tmpMount, error) { return nil, nil }

// adjustStellaHome returns the sandbox-view STELLA_HOME. No remapping on this platform.
func adjustStellaHome(stellaHome string) string { return stellaHome }

// checkSandboxRequirements is a no-op on platforms other than Linux.
func checkSandboxRequirements() error { return nil }

// wrapCommand is a no-op on platforms other than Linux and macOS.
// Commands run unwrapped on the host OS.
func wrapCommand(_ sandboxpkg.Policy, sandboxCwd string, _ []tmpMount, _ string, name string, args []string) (string, []string, string, error) {
	return name, args, sandboxCwd, nil
}
