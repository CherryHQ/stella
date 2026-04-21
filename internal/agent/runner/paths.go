package runner

import (
	"fmt"
	"path/filepath"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/resources/binaries"
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

func (p runnerPaths) toolsBinDir() string { return binaries.BinDir(p.AnnaHome) }

// resolveToolsBinDir returns the host bin dir for host-side backends (boxsh,
// local) and empty for docker, where the container image has tools pre-installed
// and host binaries are the wrong arch/OS.
func resolveToolsBinDir(paths runnerPaths, backend string) string {
	if backend == config.SandboxBackendDocker {
		return ""
	}
	return paths.toolsBinDir()
}

func (p runnerPaths) annaSkillsDir() string {
	return filepath.Join(p.AnnaHome, "skills")
}

func (p runnerPaths) annaAgentsDir() string {
	return filepath.Join(p.AnnaHome, "agents")
}

// sandboxProcessEnv builds the baseline process environment injected into
// sandboxed commands. For host-filesystem backends (boxsh, local) it pins HOME
// to the sandbox-visible writable area so CLIs don't accidentally read/write
// the host user's ~/.ssh, ~/.gitconfig, ~/.cache, etc. For docker the
// container already provides its own rootfs and image-baked HOME, so we leave
// HOME alone and let the image's user home stand — that's what lets tools
// installed in the image (mise tree, shell rc files, shims) remain reachable
// at runtime regardless of the workspace bind-mount path.
func sandboxProcessEnv(paths sandboxPaths, backend string) map[string]string {
	env := map[string]string{}
	if paths.UserRoot != "" && backend != config.SandboxBackendDocker {
		env["HOME"] = paths.UserRoot
	}
	if paths.AnnaHome != "" {
		env["ANNA_HOME"] = paths.AnnaHome
	}
	return env
}
