//go:build !linux && !darwin

package local

import (
	"os"
	"path"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On platforms other than Linux and macOS there is no path remapping.
func resolveSandboxRoot(layout hostlayout.Layout) (sandboxRoot, realRoot string) {
	real := layout.WorkspaceSource
	return real, real
}

// resolveUserDataRoot returns the shared user-data root. There is no path
// remapping on this platform, so the sandbox-space and host paths are identical.
func resolveUserDataRoot(layout hostlayout.Layout) (sandboxRoot, realRoot string) {
	for _, mount := range layout.Mounts {
		if path.Clean(mount.Target) == sandboxpkg.MountUserData {
			return mount.Source, mount.Source
		}
	}
	return "", ""
}

// createSessionTmpMounts returns an identity mount for a session-private host
// directory. Non-isolating platforms do not remap paths, but the path resolver
// still needs the published TMPDIR as a writable process-view mount.
func createSessionTmpMounts() ([]tmpMount, error) {
	tmpDir, err := os.MkdirTemp("", "stella-session-tmp-*")
	if err != nil {
		return nil, err
	}
	return []tmpMount{{sandboxPath: tmpDir, realPath: tmpDir, owned: true}}, nil
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
func wrapCommand(_ sandboxpkg.Policy, _ hostlayout.Layout, _ string, _ []tmpMount, _ string, name string, args []string) (string, []string, error) {
	return name, args, nil
}
