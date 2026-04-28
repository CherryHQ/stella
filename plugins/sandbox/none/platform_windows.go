//go:build windows

package none

func shell() (string, string) { return "cmd", "/c" }

func platformAvailable() bool { return false }
