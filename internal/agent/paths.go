package agent

import (
	"fmt"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/config"
)

// runnerPaths keeps only the runner's primary inputs.
// Everything else used by the runner is derived from these values.
type runnerPaths struct {
	StellaHome  string
	AgentRoot   string
	UserRoot    string
	ProjectRoot string
}

// sandboxPaths is the minimal path set sandbox policy creation depends on.
// Sandbox execution is defined entirely by the user-scoped writable root and an
// internal working directory derived from that root.
type sandboxPaths struct {
	StellaHome string
	UserRoot   string
	WorkDir    string
}

// resolveRunnerPaths converts GoRunnerConfig into the minimal path set the
// runner actually owns. Derived paths stay as methods/helpers instead of being
// stored as extra fields.
func resolveRunnerPaths(cfg GoRunnerConfig) runnerPaths {
	paths, _ := resolveSandboxPaths(cfg)
	return runnerPaths{
		StellaHome:  paths.StellaHome,
		AgentRoot:   cfg.AgentRoot,
		UserRoot:    paths.UserRoot,
		ProjectRoot: cfg.ProjectRoot,
	}
}

func resolveSandboxWorkingDir(cfg GoRunnerConfig, userRoot string) string {
	return userRoot
}

func resolveSandboxPaths(cfg GoRunnerConfig) (sandboxPaths, error) {
	stellaHome := cfg.StellaHome
	if stellaHome == "" {
		stellaHome = config.StellaHome()
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
		StellaHome: stellaHome,
		UserRoot:   userRoot,
		WorkDir:    workDir,
	}, nil
}

// resolveToolsBinDir always returns empty for docker: the container image has
// tools pre-installed and host binaries are the wrong arch/OS.
func resolveToolsBinDir(_ runnerPaths, _ string) string {
	return ""
}

func (p runnerPaths) stellaSkillsDir() string {
	return filepath.Join(p.StellaHome, "skills")
}

func (p runnerPaths) stellaAgentsDir() string {
	return filepath.Join(p.StellaHome, "agents")
}

// sandboxProcessEnv builds the baseline process environment injected into
// sandboxed docker commands. The docker container already provides its own
// rootfs and image-baked HOME, so we leave HOME alone and let the image's user
// home stand — that's what lets tools installed in the image (mise tree, shell
// rc files, shims) remain reachable at runtime regardless of the workspace
// bind-mount path.
func sandboxProcessEnv(paths sandboxPaths) map[string]string {
	env := map[string]string{}
	if paths.StellaHome != "" {
		env["STELLA_HOME"] = paths.StellaHome
	}
	return env
}
