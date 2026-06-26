//go:build !linux

package ml

import "os/exec"

// setParentDeathSignal is a no-op off Linux. darwin has no parent-death signal, so
// the sidecar is reaped via context cancellation on graceful shutdown; a hard
// SIGKILL of stellad can briefly orphan it until the stale-socket probe on the
// next start cleans up.
func setParentDeathSignal(_ *exec.Cmd) {}
