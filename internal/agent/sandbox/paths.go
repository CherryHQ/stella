package sandbox

import (
	"fmt"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/config"
)

// Paths is the minimal path set sandbox policy creation depends on.
// Sandbox execution is defined entirely by the user-scoped writable root and an
// internal working directory derived from that root.
type Paths struct {
	StellaHome  string
	UserRoot    string
	WorkDir     string
	AgentRoot   string
	ProjectRoot string
}

// ResolvePaths converts a Config into the minimal path set the sandbox needs.
func ResolvePaths(cfg Config) (Paths, error) {
	stellaHome := cfg.StellaHome
	if stellaHome == "" {
		stellaHome = config.StellaHome()
	}
	if cfg.UserRoot == "" {
		return Paths{}, fmt.Errorf("user_root is required")
	}

	userRoot, err := filepath.Abs(cfg.UserRoot)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user_root: %w", err)
	}
	return Paths{
		StellaHome:  stellaHome,
		UserRoot:    userRoot,
		WorkDir:     userRoot,
		AgentRoot:   cfg.AgentRoot,
		ProjectRoot: cfg.ProjectRoot,
	}, nil
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
