package sandbox

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/CherryHQ/stella/internal/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// SkillView describes how host skill roots map to the model-visible paths the
// agent sees inside the sandbox for the active backend. Isolated is true when
// host paths are not valid in-sandbox (the agent sees the remapped roots); it is
// false for host-execution backends, where host paths are used directly.
//
// It is a backend fact, owned here next to the policy/mount wiring so callers
// (the skills tool) need not know which backend is active or what its mount
// points are.
type SkillView struct {
	Isolated bool
	// SystemSkillsHost/View map the system skills dir specifically (only that
	// subtree of STELLA_HOME is mounted; the sibling users/ and agents/ trees
	// nested under STELLA_HOME are not, so a broad mapping would mis-map them).
	SystemSkillsHost string
	SystemSkillsView string
	UserDataHost     string
	UserDataView     string
	WorkspaceHost    string
	WorkspaceView    string
}

// ResolveSkillView returns the skill-root remapping for cfg's active backend:
//   - linux local (bwrap): isolating — host roots map to /opt/stella, /user, /workspace.
//   - none, and macOS local (Seatbelt): identity — the host filesystem is shared,
//     so host paths are valid in-sandbox as-is.
//   - docker: isolating with the same two-root container layout as bwrap
//     (/opt/stella, /user, /workspace), so host roots map identically.
func ResolveSkillView(ctx context.Context, cfg Config, paths Paths) SkillView {
	backend := resolveBackendName(ctx, cfg)
	systemSkillsHost := filepath.Join(paths.StellaHome, ".agents", "skills")
	v := SkillView{
		SystemSkillsHost: systemSkillsHost,
		SystemSkillsView: systemSkillsHost,
		UserDataHost:     paths.UserDataDir,
		UserDataView:     paths.UserDataDir,
		WorkspaceHost:    paths.WorkspaceRoot,
		WorkspaceView:    paths.WorkspaceRoot,
	}
	// Linux bwrap (local) and docker both remap host roots to the two-root
	// container layout. macOS local (Seatbelt) and none share the host
	// filesystem, so they stay identity.
	isolating := backend == config.SandboxBackendDocker ||
		(backend == config.SandboxBackendLocal && runtime.GOOS == "linux")
	if isolating {
		v.Isolated = true
		v.SystemSkillsView = filepath.Join(pkgsandbox.MountStellaHome, ".agents", "skills")
		v.UserDataView = pkgsandbox.MountUserData
		v.WorkspaceView = pkgsandbox.MountWorkspace
	}
	return v
}
