package runner

import (
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// runnerPaths keeps only the runner's primary inputs.
// Everything else used by the runner is derived from these values.
type runnerPaths struct {
	AnnaHome  string
	UserHome  string
	AgentRoot string
	UserRoot  string
	WorkDir   string
}

// resolveRunnerPaths converts GoRunnerConfig into the minimal path set the
// runner actually owns. Derived paths stay as methods/helpers instead of being
// stored as extra fields.
func resolveRunnerPaths(cfg GoRunnerConfig) runnerPaths {
	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}

	userHome, _ := os.UserHomeDir()
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = cfg.UserRoot
	}

	return runnerPaths{
		AnnaHome:  annaHome,
		UserHome:  userHome,
		AgentRoot: cfg.AgentRoot,
		WorkDir:   workDir,
		UserRoot:  cfg.UserRoot,
	}
}

func (p runnerPaths) sandboxRoot() string { return p.UserRoot }
func (p runnerPaths) processHome() string { return p.UserRoot }
func (p runnerPaths) toolsBinDir() string { return embedded.BinDir(p.AnnaHome) }
func (p runnerPaths) builtinSkillsDir() string {
	return filepath.Join(p.AnnaHome, "cache", "builtin-skills")
}

// sandboxRoot returns the directory mounted as the sandbox root.
// Runner execution is always user-scoped, so this is always UserRoot.
func sandboxRoot(cfg GoRunnerConfig) string {
	return resolveRunnerPaths(cfg).sandboxRoot()
}

// sandboxProcessEnv builds the baseline process environment injected into
// sandboxed commands. Today it pins HOME to the sandbox-visible writable area
// and propagates ANNA_HOME so CLIs don't accidentally target the host home.
func sandboxProcessEnv(paths runnerPaths) map[string]string {
	env := map[string]string{}
	if home := paths.processHome(); home != "" {
		env["HOME"] = home
	}
	if paths.AnnaHome != "" {
		env["ANNA_HOME"] = paths.AnnaHome
	}
	return env
}
