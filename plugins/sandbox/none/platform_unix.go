//go:build !windows

package none

func shell() (string, string) { return "sh", "-c" }

func platformAvailable() bool { return true }
