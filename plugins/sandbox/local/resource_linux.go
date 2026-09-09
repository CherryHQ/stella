//go:build linux

package local

// resourceCloseProof is available for the bwrap backend once every tracked
// bwrap owner has been reaped. The unshared PID namespace is torn down with
// its namespace owner, so an empty local process set then proves the session
// resource is absent.
func resourceCloseProof() bool { return true }
