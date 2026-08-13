//go:build !linux && !darwin

package local

import (
	"os"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func processVisiblePath(_ string, hostPath string) string { return hostPath }

// createSessionTmpMounts returns an identity mount for a session-private host
// directory. Non-isolating platforms do not remap paths, but the path resolver
// still needs the published TMPDIR as a writable process-view mount.
func createSessionTmpMounts() ([]tmpMount, error) {
	tmpDir, err := os.MkdirTemp("", "stella-session-tmp-*")
	if err != nil {
		return nil, err
	}
	return []tmpMount{{sandboxPath: tmpDir, realPath: tmpDir, owned: true, environment: true}}, nil
}

// filesystemTempDir returns the real temporary directory from the identity
// process-view mount, falling back to the host temporary directory safely.
func filesystemTempDir(mounts []tmpMount) string {
	for _, mount := range mounts {
		if mount.environment && mount.sandboxPath == mount.realPath && mount.realPath != "" {
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
func (*localSession) wrapCommand(_ sandboxpkg.Policy, _, name string, args []string) (string, []string, error) {
	return name, args, nil
}
