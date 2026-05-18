package sandbox

import (
	"fmt"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/config"
)

// Paths is the path set sandbox policy creation and tool registration depend on.
type Paths struct {
	StellaHome  string
	AgentRoot   string
	UserRoot    string
	ProjectRoot string
	// WorkDir is the initial working directory inside the sandbox.
	// Set by ResolvePaths to the absolute form of UserRoot.
	WorkDir string
}

// ResolvePaths validates cfg.Paths and fills derived fields (StellaHome default, WorkDir).
func ResolvePaths(cfg Config) (Paths, error) {
	p := cfg.Paths
	if p.StellaHome == "" {
		p.StellaHome = config.StellaHome()
	}
	if p.AgentRoot == "" {
		return Paths{}, fmt.Errorf("agent_root is required")
	}
	if p.UserRoot == "" {
		return Paths{}, fmt.Errorf("user_root is required")
	}
	userRoot, err := filepath.Abs(p.UserRoot)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user_root: %w", err)
	}
	p.UserRoot = userRoot
	p.WorkDir = userRoot
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
