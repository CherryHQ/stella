package runner

import (
	"fmt"
	"path/filepath"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// runnerPaths keeps only the runner's primary inputs.
// Everything else used by the runner is derived from these values.
type runnerPaths struct {
	AnnaHome    string
	AgentRoot   string
	UserRoot    string
	ProjectRoot string
}

// sandboxPaths is the minimal path set sandbox policy creation depends on.
// Sandbox execution is defined entirely by the user-scoped writable root and an
// internal working directory derived from that root.
type sandboxPaths struct {
	AnnaHome string
	UserRoot string
	WorkDir  string
}

// resolveRunnerPaths converts GoRunnerConfig into the minimal path set the
// runner actually owns. Derived paths stay as methods/helpers instead of being
// stored as extra fields.
func resolveRunnerPaths(cfg GoRunnerConfig) runnerPaths {
	paths, _ := resolveSandboxPaths(cfg)
	return runnerPaths{
		AnnaHome:    paths.AnnaHome,
		AgentRoot:   cfg.AgentRoot,
		UserRoot:    paths.UserRoot,
		ProjectRoot: cfg.ProjectRoot,
	}
}

func resolveSandboxWorkingDir(cfg GoRunnerConfig, userRoot string) string {
	return userRoot
}

func resolveSandboxPaths(cfg GoRunnerConfig) (sandboxPaths, error) {
	annaHome := cfg.AnnaHome
	if annaHome == "" {
		annaHome = config.AnnaHome()
	}
	if cfg.UserRoot == "" {
		return sandboxPaths{}, fmt.Errorf("user_root is required")
	}

	userRoot, err := filepath.Abs(cfg.UserRoot)
	if err != nil {
		return sandboxPaths{}, fmt.Errorf("resolve user_root: %w", err)
	}
	workDir := resolveSandboxWorkingDir(cfg, userRoot)

	return sandboxPaths{
		AnnaHome: annaHome,
		UserRoot: userRoot,
		WorkDir:  workDir,
	}, nil
}

func (p runnerPaths) toolsBinDir() string { return embedded.BinDir(p.AnnaHome) }

func (p runnerPaths) annaSkillsDir() string {
	return filepath.Join(p.AnnaHome, "skills")
}

func (p runnerPaths) annaAgentsDir() string {
	return filepath.Join(p.AnnaHome, "agents")
}


// sandboxProcessEnv builds the baseline process environment injected into
// sandboxed commands. Today it pins HOME to the sandbox-visible writable area
// and propagates ANNA_HOME so CLIs don't accidentally target the host home.
func sandboxProcessEnv(paths sandboxPaths) map[string]string {
	env := map[string]string{}
	if paths.UserRoot != "" {
		env["HOME"] = paths.UserRoot
	}
	if paths.AnnaHome != "" {
		env["ANNA_HOME"] = paths.AnnaHome
	}
	return env
}
