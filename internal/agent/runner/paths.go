package runner

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// runnerPaths keeps only the runner's primary inputs.
// Everything else used by the runner is derived from these values.
type runnerPaths struct {
	AnnaHome  string
	AgentRoot string
	UserRoot  string
	WorkDir   string
}

// sandboxPaths is the minimal path set sandbox policy creation depends on.
// Sandbox execution is defined entirely by the user-scoped writable root and a
// working directory constrained to that root.
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
		AnnaHome:  paths.AnnaHome,
		AgentRoot: cfg.AgentRoot,
		UserRoot:  paths.UserRoot,
		WorkDir:   paths.WorkDir,
	}
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
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = userRoot
	}
	if !filepath.IsAbs(workDir) {
		workDir = filepath.Join(userRoot, workDir)
	}
	workDir = filepath.Clean(workDir)

	if !isWithinPathRoot(userRoot, workDir) {
		return sandboxPaths{}, fmt.Errorf("work_dir %q must stay within user_root %q", workDir, userRoot)
	}

	return sandboxPaths{
		AnnaHome: annaHome,
		UserRoot: userRoot,
		WorkDir:  workDir,
	}, nil
}

func (p runnerPaths) toolsBinDir() string { return embedded.BinDir(p.AnnaHome) }
func (p runnerPaths) builtinSkillsDir() string {
	return filepath.Join(p.AnnaHome, "cache", "builtin-skills")
}

// sandboxRoot returns the directory mounted as the sandbox root.
// Runner execution is always user-scoped, so this is always UserRoot.
func sandboxRoot(cfg GoRunnerConfig) string {
	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		return ""
	}
	return paths.UserRoot
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

func isWithinPathRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
