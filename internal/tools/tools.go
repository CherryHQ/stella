package tools

import (
	"os"
	"os/exec"
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

// ResolveBinary returns the path to a named binary, checking the anna bin
// directory first, then $PATH. Returns empty string if not found anywhere.
// Use this instead of rolling per-plugin path resolution.
func ResolveBinary(binDir, name string) string {
	if binDir != "" {
		p := filepath.Join(binDir, binaryFileName(name))
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

