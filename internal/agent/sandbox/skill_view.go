package sandbox

import (
	"context"
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
	Isolated          bool
	BuiltinSkillsHost string
	BuiltinSkillsView string
	UserDataHost      string
	UserDataView      string
	WorkspaceHost     string
	WorkspaceView     string
}

// ResolveSkillView returns the skill-root remapping for cfg's active backend:
//   - linux local (bwrap): isolating — host roots map to /opt/stella, /user, /workspace.
//   - none, and macOS local (Seatbelt): identity — the host filesystem is shared,
//     so host paths are valid in-sandbox as-is.
//   - docker: isolating with the same two-root container layout as bwrap
//     (/opt/stella, /user, /workspace), so host roots map identically.
func ResolveSkillView(ctx context.Context, cfg Config, paths Paths) SkillView {
	backend := resolveBackendName(ctx, cfg)
	v := SkillView{
		BuiltinSkillsHost: paths.BuiltinBundle,
		BuiltinSkillsView: paths.BuiltinBundle,
		UserDataHost:      paths.UserDataDir,
		UserDataView:      paths.UserDataDir,
		WorkspaceHost:     paths.WorkspaceRoot,
		WorkspaceView:     paths.WorkspaceRoot,
	}
	if isolatingBackend(backend) {
		v.Isolated = true
		v.BuiltinSkillsView = pkgsandbox.MountBuiltinSkills
		v.UserDataView = pkgsandbox.MountUserData
		v.WorkspaceView = pkgsandbox.MountWorkspace
	}
	return v
}

// isolatingBackend reports whether the active backend remaps host roots to the
// two-root container layout (the agent sees /opt/stella, /user, /workspace).
// Linux bwrap (local) and docker do; macOS local (Seatbelt) and none share the
// host filesystem, so host paths are valid in-sandbox as-is.
func isolatingBackend(backend string) bool {
	return backend == config.SandboxBackendDocker ||
		(backend == config.SandboxBackendLocal && runtime.GOOS == "linux")
}

// WorkspaceViewFor maps a host workspace root to the path the agent actually
// sees for the given backend: MountWorkspace (/workspace) on isolating backends,
// the host path unchanged otherwise. Use it to label workspace paths for the
// user without reconstructing the isolating-backend decision.
func WorkspaceViewFor(backend, hostWorkspaceRoot string) string {
	if isolatingBackend(backend) {
		return pkgsandbox.MountWorkspace
	}
	return hostWorkspaceRoot
}

// UserDataViewFor maps a host user-data root to the path the agent sees for the
// given backend: MountUserData (/user) on isolating backends, the host path
// unchanged otherwise. The user-data counterpart of WorkspaceViewFor.
func UserDataViewFor(backend, hostUserDataRoot string) string {
	if isolatingBackend(backend) {
		return pkgsandbox.MountUserData
	}
	return hostUserDataRoot
}
