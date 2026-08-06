//go:build unix && !darwin && !linux

package main

import "errors"

func identityFor(int) (processIdentity, error) {
	return processIdentity{}, errors.New("agent-test lifecycle is supported only on Linux and macOS")
}
