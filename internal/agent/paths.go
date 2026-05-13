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

// resolveRunnerPaths converts GoRunnerConfig into the minimal path set the
// runner actually owns. Derived paths stay as methods/helpers instead of being
// stored as extra fields.
func resolveRunnerPaths(cfg GoRunnerConfig) runnerPaths {
	paths, _ := sandbox.ResolvePaths(sandbox.Config{
		StellaHome: cfg.StellaHome,
		UserRoot:   cfg.UserRoot,
	})
	return runnerPaths{
		StellaHome:  paths.StellaHome,
		AgentRoot:   cfg.AgentRoot,
		UserRoot:    paths.UserRoot,
		ProjectRoot: cfg.ProjectRoot,
	}
}

// resolveToolsBinDir always returns empty for docker: the container image has
// tools pre-installed and host binaries are the wrong arch/OS.
func resolveToolsBinDir(_ runnerPaths, _ string) string {
	return ""
}

func (p runnerPaths) stellaSkillsDir() string {
	return filepath.Join(p.StellaHome, ".agents", "skills")
}

func (p runnerPaths) stellaAgentsDir() string {
	return filepath.Join(p.StellaHome, ".agents", "agents")
}
