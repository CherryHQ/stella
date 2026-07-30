//go:build !linux && !darwin

package local

import (
	"os"

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

// createSessionTmpMounts returns the identity mount for the host temporary
// directory. Non-isolating platforms do not remap paths, but the path resolver
// still needs the published TMPDIR as a writable process-view mount.
func createSessionTmpMounts(policy sandboxpkg.Policy) ([]tmpMount, error) {
	tmpDir := policy.Filesystem.TempDirHost
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	return []tmpMount{{sandboxPath: tmpDir, realPath: tmpDir}}, nil
}

// filesystemTempDir returns the real temporary directory from the identity
// process-view mount, falling back to the host temporary directory safely.
func filesystemTempDir(mounts []tmpMount) string {
	for _, mount := range mounts {
		if mount.sandboxPath == mount.realPath && mount.realPath != "" {
			return mount.realPath
		}
	}
	return os.TempDir()
}

// adjustStellaHome returns the sandbox-view STELLA_HOME. No remapping on this platform.
func adjustStellaHome(stellaHome string) string { return stellaHome }

// checkSandboxRequirements is a no-op on platforms other than Linux.
func checkSandboxRequirements() error { return nil }

// wrapCommand is a no-op on platforms other than Linux and macOS.
// Commands run unwrapped on the host OS.
func wrapCommand(_ sandboxpkg.Policy, _ string, _ []tmpMount, _ string, name string, args []string) (string, []string, error) {
	return name, args, nil
}
