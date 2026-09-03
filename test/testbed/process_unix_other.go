//go:build unix && !darwin && !linux

package testbed

import "errors"

func identityFor(int) (processIdentity, error) {
	return processIdentity{}, errors.New("the Stella testbed is supported only on Linux and macOS")
}
