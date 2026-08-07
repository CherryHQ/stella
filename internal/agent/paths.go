package agent

import "github.com/CherryHQ/stella/internal/agent/sandbox"

// resolveToolsBinDir always returns empty for docker: the container image has
// tools pre-installed and host binaries are the wrong arch/OS.
func resolveToolsBinDir(_ sandbox.Paths, _ string) string {
	return ""
}
