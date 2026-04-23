package tools

import (
	"os"
	"path/filepath"
	"runtime"
)

// BinDir returns the tools binary directory path within annaHome.
func BinDir(annaHome string) string {
	return filepath.Join(annaHome, "bin")
}

// binaryFileName returns the platform-appropriate binary file name (appends .exe on Windows).
func binaryFileName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ToolPath returns the full path to a named downloadable tool, or empty if not installed.
func ToolPath(annaHome, name string) string {
	p := filepath.Join(BinDir(annaHome), binaryFileName(name))
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

