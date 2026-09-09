//go:build !linux

package local

// resourceCloseProof is unavailable on native backends without a resource
// boundary that accounts for detached descendants. An empty process map is
// therefore not cleanup proof on Darwin, Windows, or other hosts.
func resourceCloseProof() bool { return false }
