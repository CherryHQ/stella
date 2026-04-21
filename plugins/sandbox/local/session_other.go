//go:build !linux && !darwin

package local

import (
	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// resolveSandboxRoot returns the sandbox-space root and the real host root.
// On platforms other than Linux and macOS there is no path remapping.
func resolveSandboxRoot(policy sandboxpkg.Policy) (sandboxRoot, realRoot string) {
	real := policy.WorkspaceRootOrDefault()
	return real, real
}

// wrapCommand is a no-op on platforms other than Linux and macOS.
// Commands run unwrapped on the host OS.
func wrapCommand(_ sandboxpkg.Policy, sandboxCwd, name string, args []string) (string, []string, string, error) {
	return name, args, sandboxCwd, nil
}
