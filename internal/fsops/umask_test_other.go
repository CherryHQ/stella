//go:build !darwin && !linux

package fsops

func setTestUmask(mask int) int { return 0 }
