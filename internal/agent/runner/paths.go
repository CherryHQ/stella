package runner

import (
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// runnerPaths is the runner's normalized view of all path-like config.
//
// The intent is to separate a few similar-but-different concepts:
//   - user home on the host machine (for shared discovery like ~/.agents)
//   - agent workspace (the agent's persistent working area)
//   - per-user data dir (user-scoped state inside an agent workspace)
//   - sandbox root (the filesystem root exposed to sandboxed tools)
//   - process home (the HOME env seen by commands executed in the sandbox)
//
// In practice:
//   - UserHome is used for host-side discovery of shared skills/agents.
//   - AgentRoot is the agent-level persistent directory.
//   - UserRoot is the user-scoped persistent directory and required for execution.
//   - SandboxRoot is always the user-scoped writable root.
//   - ProcessHome is exported as HOME for CLI tools running in the sandbox.
//
// This keeps call sites from having to re-decide which of cfg.AgentRoot,
// cfg.UserRoot, cfg.WorkDir, cfg.AnnaHome, or os.UserHomeDir() should be
// used for each operation.
type runnerPaths struct {
	// AnnaHome is Anna's resolved home directory.
	// Used for managed binaries, cache, builtin skills, and ANNA_HOME.
	AnnaHome string

	// UserHome is the real host user home directory from os.UserHomeDir().
	// Used for host-side shared discovery like ~/.agents/{skills,agents}.
	UserHome string

	// AgentRoot is the agent's persistent root directory.
	// Used for agent-scoped files such as agent-local skills/agents.
	AgentRoot string

	// WorkDir is the requested working directory for tool execution.
	// When empty, it defaults to UserRoot.
	WorkDir string

	// UserRoot is the user-scoped persistent storage within an agent.
	// Runner execution is always user-scoped, so this must be set.
	UserRoot string

	// SandboxRoot is the directory exposed as the sandbox's writable root.
	// It always resolves to UserRoot.
	SandboxRoot string

	// ProcessHome is the HOME env visible to executed CLIs.
	// It always resolves to UserRoot.
	ProcessHome string

	// ToolsBinDir is Anna's managed bin directory.
	// Used to expose embedded helper binaries to tools and sandboxes.
	ToolsBinDir string

	// BuiltinSkillsDir is the extracted builtin skills cache directory.
	// Used for builtin preset/skill discovery.
	BuiltinSkillsDir string
}

// resolveRunnerPaths converts GoRunnerConfig into one normalized path bundle.
//
// Resolution rules:
//   - AnnaHome: cfg.AnnaHome or config.AnnaHome()
//   - UserHome: os.UserHomeDir()
//   - SandboxRoot: cfg.UserRoot
//   - ProcessHome: cfg.UserRoot
//   - WorkDir: cfg.WorkDir if set, else cfg.UserRoot
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
		AnnaHome:         annaHome,
		UserHome:         userHome,
		AgentRoot:        cfg.AgentRoot,
		WorkDir:          workDir,
		UserRoot:         cfg.UserRoot,
		SandboxRoot:      cfg.UserRoot,
		ProcessHome:      cfg.UserRoot,
		ToolsBinDir:      embedded.BinDir(annaHome),
		BuiltinSkillsDir: filepath.Join(annaHome, "cache", "builtin-skills"),
	}
}

// sandboxRoot returns the directory mounted as the sandbox root.
// Runner execution is always user-scoped, so this is always UserRoot.
func sandboxRoot(cfg GoRunnerConfig) string {
	return resolveRunnerPaths(cfg).SandboxRoot
}

// sandboxProcessEnv builds the baseline process environment injected into
// sandboxed commands. Today it pins HOME to the sandbox-visible writable area
// and propagates ANNA_HOME so CLIs don't accidentally target the host home.
func sandboxProcessEnv(paths runnerPaths) map[string]string {
	env := map[string]string{}
	if paths.ProcessHome != "" {
		env["HOME"] = paths.ProcessHome
	}
	if paths.AnnaHome != "" {
		env["ANNA_HOME"] = paths.AnnaHome
	}
	return env
}
