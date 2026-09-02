package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/resources"
)

// Paths is the path set sandbox policy creation and tool registration depend on.
type Paths struct {
	StellaHome    string
	BuiltinBundle string
	AgentRoot     string
	UserRoot      string
	// WorkspaceRoot is the agent's private workspace root — sandbox HOME and cwd
	// in the two-root layout.
	WorkspaceRoot string
	// UserDataDir is the shared principal data root, mounted as /user by
	// isolating backends.
	UserDataDir string
	ProjectRoot string
	// WorkDir is the initial working directory: WorkspaceRoot, or ProjectRoot
	// when that project is contained by the agent workspace.
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
	registry, err := resources.Default()
	if err != nil {
		return Paths{}, fmt.Errorf("load builtin skill bundle: %w", err)
	}
	p.BuiltinBundle, err = registry.BundlePath(p.StellaHome)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve builtin skill bundle: %w", err)
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
	expectedWorkspace := workspaceRoot(userRoot, cfg)
	if resolved, err := filepath.EvalSymlinks(expectedWorkspace); err == nil {
		expectedWorkspace = resolved
	}
	switch {
	case p.WorkspaceRoot == "":
		p.WorkspaceRoot = expectedWorkspace
	case !filepath.IsAbs(p.WorkspaceRoot):
		return Paths{}, fmt.Errorf("workspace_root must be absolute")
	default:
		if resolved, err := filepath.EvalSymlinks(p.WorkspaceRoot); err == nil {
			p.WorkspaceRoot = resolved
		}
		if filepath.Clean(p.WorkspaceRoot) != filepath.Clean(expectedWorkspace) {
			return Paths{}, fmt.Errorf("workspace_root must match the authorized agent workspace")
		}
	}
	expectedDataDir := filepath.Join(userRoot, "data")
	if resolved, err := filepath.EvalSymlinks(expectedDataDir); err == nil {
		expectedDataDir = resolved
	}
	switch {
	case p.UserDataDir == "":
		p.UserDataDir = expectedDataDir
	case !filepath.IsAbs(p.UserDataDir):
		return Paths{}, fmt.Errorf("user_data_dir must be absolute")
	default:
		if resolved, err := filepath.EvalSymlinks(p.UserDataDir); err == nil {
			p.UserDataDir = resolved
		}
		if filepath.Clean(p.UserDataDir) != filepath.Clean(expectedDataDir) {
			return Paths{}, fmt.Errorf("user_data_dir must match the authorized principal data root")
		}
	}
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
	env := map[string]string{}
	if paths.StellaHome != "" {
		env["STELLA_HOME"] = paths.StellaHome
	}
	return env
}
