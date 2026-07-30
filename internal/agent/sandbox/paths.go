package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
)

// Paths is the path set sandbox policy creation and tool registration depend on.
type Paths struct {
	StellaHome string
	AgentRoot  string
	UserRoot   string
	// WorkspaceRoot is the agent's workspace root — the sandbox HOME and cwd in
	// the two-root layout. During the migration it tracks UserRoot (the user home)
	// and no consumer reads it yet; a later phase flips it to the per-agent dir.
	WorkspaceRoot string
	// UserDataDir is the shared user-data root (mounted as /user). Filled from
	// UserRoot/data; not yet consumed during the migration.
	UserDataDir string
	ProjectRoot string
	// WorkDir is the initial working directory inside the sandbox.
	// Set by ResolvePaths to the absolute form of UserRoot.
	WorkDir string
}

// ResolvePaths validates cfg.Paths and fills derived fields (StellaHome default, WorkDir).
// When ProjectRoot is set and is a valid subpath of UserRoot, WorkDir is set to ProjectRoot.
func ResolvePaths(cfg Config) (Paths, error) {
	p := cfg.Paths
	if p.StellaHome == "" {
		p.StellaHome = config.StellaHome()
	}
	if resolved, err := filepath.EvalSymlinks(p.StellaHome); err == nil {
		p.StellaHome = resolved
	}
	if p.AgentRoot == "" {
		return Paths{}, fmt.Errorf("agent_root is required")
	}
	if resolved, err := filepath.EvalSymlinks(p.AgentRoot); err == nil {
		p.AgentRoot = resolved
	}
	if p.UserRoot == "" {
		return Paths{}, fmt.Errorf("user_root is required")
	}
	userRoot, err := filepath.Abs(p.UserRoot)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user_root: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(userRoot); err == nil {
		userRoot = resolved
	}
	p.UserRoot = userRoot
	// A principal (user/group) session must name its agent: the workspace is the
	// per-agent dir and isolation is by mounting only that dir. With a principal
	// but no AgentID the workspace would fall back to the whole user home, which —
	// now that sibling-hiding is gone — would re-expose every sibling agent.
	if cfg.AgentID == "" && (cfg.UserID != "" || cfg.GroupID != "") {
		return Paths{}, fmt.Errorf("agent_id is required for a user or group session")
	}
	// Two-root layout: WorkspaceRoot is the per-agent dir (sandbox HOME/cwd =
	// /workspace), UserDataDir is the shared user-data root (mounted as /user).
	// A user-less job has no principal home, so its workspace is the home itself.
	p.WorkspaceRoot = workspaceRoot(userRoot, cfg)
	if resolved, err := filepath.EvalSymlinks(p.WorkspaceRoot); err == nil {
		p.WorkspaceRoot = resolved
	}
	p.UserDataDir = filepath.Join(userRoot, "data")
	p.WorkDir = p.WorkspaceRoot

	if p.ProjectRoot != "" {
		pr, err := filepath.Abs(p.ProjectRoot)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve project_root: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(pr); err == nil {
			pr = resolved
		}
		// Projects live under the agent workspace, so validate against it.
		rel, relErr := filepath.Rel(p.WorkspaceRoot, pr)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			p.ProjectRoot = pr
			p.WorkDir = pr
		}
	}

	return p, nil
}

// workspaceRoot returns the agent's workspace root within the user home: the
// per-(principal, agent) dir users/{id}/agents/{agentID}. A user-less job (no
// principal, e.g. a builtin scheduled job) has no per-agent subdir — its home is
// its own workspace — so the home is returned unchanged. Mirrors the on-disk
// layout in internal/agent/workspace.go, joined literally here to avoid a cycle.
func workspaceRoot(userRoot string, cfg Config) string {
	if cfg.AgentID == "" || (cfg.UserID == "" && cfg.GroupID == "") {
		return userRoot
	}
	return filepath.Join(userRoot, "agents", cfg.AgentID)
}

// ProcessEnv builds runner-owned process environment injected into every
// sandbox backend. Filesystem roots belong to the backend because each backend
// presents a different filesystem view.
func ProcessEnv(paths Paths) map[string]string {
	env := make(map[string]string, 1)
	if paths.StellaHome != "" {
		env["STELLA_HOME"] = paths.StellaHome
	}
	return env
}
