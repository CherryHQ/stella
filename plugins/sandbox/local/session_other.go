//go:build !linux && !darwin

package local

import (
	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// wrapCommand is a no-op on platforms other than Linux and macOS.
// Commands run unwrapped on the host OS.
func wrapCommand(_ sandboxpkg.Policy, name string, args []string) (string, []string, error) {
	return name, args, nil
}
