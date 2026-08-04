//go:build linux

package local

import (
	"os"
	"testing"
)

// requireBwrapEnv lets an environment that promises a working bwrap turn the
// skip below into a failure. Skipping is right for hosts that legitimately
// cannot create user namespaces (unprivileged containers, AppArmor-restricted
// distros), but on CI a silent skip is indistinguishable from coverage
// quietly disappearing — which is exactly what happened for three months.
const requireBwrapEnv = "STELLA_TEST_REQUIRE_BWRAP"

// skipIfBwrapNotFunctional skips the test when bwrap is not installed or cannot
// create user namespaces (e.g. AppArmor restriction on Ubuntu 24.04+), unless
// STELLA_TEST_REQUIRE_BWRAP is set, in which case it fails.
func skipIfBwrapNotFunctional(t *testing.T) {
	t.Helper()
	if bwrapFunctional() {
		return
	}
	const reason = "bwrap not functional on this Linux host (install bubblewrap or enable unprivileged user namespaces)"
	if os.Getenv(requireBwrapEnv) != "" {
		t.Fatalf("%s, but %s is set", reason, requireBwrapEnv)
	}
	t.Skip(reason)
}
