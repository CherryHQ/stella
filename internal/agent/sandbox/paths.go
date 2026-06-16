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
	p.WorkDir = userRoot
	// Additive (migration phase 1): fill the two-root fields without consuming
	// them. WorkspaceRoot tracks UserRoot for now; UserDataDir is UserRoot/data.
	p.WorkspaceRoot = userRoot
	p.UserDataDir = filepath.Join(userRoot, "data")

	if p.ProjectRoot != "" {
		pr, err := filepath.Abs(p.ProjectRoot)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve project_root: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(pr); err == nil {
			pr = resolved
		}
		rel, relErr := filepath.Rel(userRoot, pr)
		if relErr == nil && !strings.HasPrefix(rel, "..") {
			p.ProjectRoot = pr
			p.WorkDir = pr
		}
	}

	return p, nil
}

// ProcessEnv builds the baseline process environment injected into
// sandboxed docker commands. The docker container already provides its own
// rootfs and image-baked HOME, so we leave HOME alone and let the image's user
// home stand — that's what lets tools installed in the image (mise tree, shell
// rc files, shims) remain reachable at runtime regardless of the workspace
// bind-mount path.
func ProcessEnv(paths Paths) map[string]string {
	env := map[string]string{}
	if paths.StellaHome != "" {
		env["STELLA_HOME"] = paths.StellaHome
	}
	return env
}
