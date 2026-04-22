//go:build darwin

package local

import (
	"fmt"
	"os/exec"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On macOS there is no path remapping, so both are always identical.
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	real := policy.WorkspaceRootOrDefault()
	return real, real
}

// wrapCommand is a no-op on macOS.
//
// The local backend now runs commands directly on the host OS without applying
// sandbox-exec or any other macOS-specific isolation layer. Filesystem and
// network policy enforcement is therefore not provided on this platform when
// the local backend is active.
func wrapCommand(_ sandboxpkg.Policy, sandboxCwd, name string, args []string) (execPath string, execArgs []string, hostCwd string, err error) {
	resolved, lookErr := exec.LookPath(name)
	if lookErr != nil {
		return "", nil, "", fmt.Errorf("local exec: look up %q: %w", name, lookErr)
	}

	passthroughArgs := append([]string(nil), args...)
	return resolved, passthroughArgs, sandboxCwd, nil
}
