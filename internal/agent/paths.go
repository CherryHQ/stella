package agent

import (
	"path/filepath"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
)

// resolveToolsBinDir always returns empty for docker: the container image has
// tools pre-installed and host binaries are the wrong arch/OS.
func resolveToolsBinDir(_ sandbox.Paths, _ string) string {
	return ""
}

func stellaAgentsDir(paths sandbox.Paths) string {
	return filepath.Join(paths.StellaHome, ".agents", "agents")
}
