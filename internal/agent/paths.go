package agent

import (
	"path/filepath"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
)

// runnerPaths keeps only the runner's primary inputs.
// Everything else used by the runner is derived from these values.
type runnerPaths struct {
	StellaHome  string
	AgentRoot   string
	UserRoot    string
	ProjectRoot string
}

// resolveRunnerPaths converts sandbox path config into the minimal path set
// the runner actually owns. Derived paths stay as methods/helpers instead of
// being stored as extra fields.
func resolveRunnerPaths(pc sandbox.PathConfig) runnerPaths {
	paths, _ := sandbox.ResolvePaths(sandbox.Config{
		Paths: sandbox.PathConfig{
			StellaHome: pc.StellaHome,
			UserRoot:   pc.UserRoot,
		},
	})
	return runnerPaths{
		StellaHome:  paths.StellaHome,
		AgentRoot:   pc.AgentRoot,
		UserRoot:    paths.UserRoot,
		ProjectRoot: pc.ProjectRoot,
	}
}

// resolveToolsBinDir always returns empty for docker: the container image has
// tools pre-installed and host binaries are the wrong arch/OS.
func resolveToolsBinDir(_ runnerPaths, _ string) string {
	return ""
}

func (p runnerPaths) stellaAgentsDir() string {
	return filepath.Join(p.StellaHome, ".agents", "agents")
}
