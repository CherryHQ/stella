//go:build linux

package local

import "testing"

// skipIfBwrapNotFunctional skips the test when bwrap is not installed or cannot
// create user namespaces (e.g. AppArmor restriction on Ubuntu 24.04+).
func skipIfBwrapNotFunctional(t *testing.T) {
	t.Helper()
	if !bwrapFunctional() {
		t.Skip("bwrap not functional on this Linux host (install bubblewrap or enable unprivileged user namespaces)")
	}
}
