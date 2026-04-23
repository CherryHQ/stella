package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ResolveBinary returns the path to a named binary, checking binDir first,
// then $PATH. Returns empty string if not found. Plugins receive binDir via
// their context (e.g. HookContext.ToolsBinDir).
func ResolveBinary(binDir, name string) string {
	if binDir != "" {
		p := filepath.Join(binDir, binaryName(name))
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
