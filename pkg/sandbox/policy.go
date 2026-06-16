package sandbox

import (
	"errors"
	"fmt"
	"time"
)

// NetworkMode defines the network access mode for a sandbox session.
type NetworkMode string

const (
	// NetworkDisabled blocks all network access.
	NetworkDisabled NetworkMode = "disabled"
	// NetworkAllowAll allows unrestricted network access.
	NetworkAllowAll NetworkMode = "allow_all"
)

// Mount points an isolating backend exposes inside the sandbox. These are the
// stable, model-visible paths for the two-root layout (plus the read-only system
// install tree). The bwrap backend binds the host roots here; higher layers
// (e.g. the skills tool's skill_dir remap) address skills by these paths instead
// of host paths. Backends that don't isolate the filesystem (none) ignore them
// and use host paths directly.
const (
	// MountWorkspace is the agent's per-agent workspace root and HOME/cwd.
	MountWorkspace = "/workspace"
	// MountUserData is the shared user-data root (toolchain, caches, skills, uploads).
	MountUserData = "/user"
	// MountStellaHome is the read-only system install tree (STELLA_HOME).
	MountStellaHome = "/opt/stella"
	// MountAgentSkills is the read-only mount of the admin-managed, agent-bound
	// (system_agent scope) skills dir. It lives outside the two roots (its host
	// dir is the user-independent agent definition tree, not the per-agent
	// workspace), so it gets its own fixed mount instead of mapping onto /workspace.
	MountAgentSkills = "/opt/stella/agent-skills"
)

// Policy is an immutable, backend-agnostic session policy describing requested limits
// for filesystem, network, and process constraints.
type Policy struct {
	// Filesystem policy
	Filesystem FilesystemPolicy

	// Network policy
	Network NetworkPolicy

	// Env holds environment variables injected into sandboxed processes.
	Env map[string]string

	// InheritEnv includes system environment variables when true.
	InheritEnv bool

	// Timeout for process execution. Zero means no timeout.
	Timeout time.Duration
}

// FilesystemPolicy defines filesystem constraints for a sandbox session.
type FilesystemPolicy struct {
	// WorkspaceRoot is the host path mounted as the sandbox root. When empty,
	// WorkingDir is used for backwards compatibility.
	WorkspaceRoot string

	// WorkingDir is the logical working directory inside the sandbox root.
	WorkingDir string

	// UserDataDir is the host path of the shared user-data root, mounted as a
	// second top-level root (/user) in the two-root layout. Holds toolchains,
	// caches, user-level skills/delegates, and uploads shared across the user's
	// agents. Empty during the migration / for backends that don't yet implement
	// the second root; not consumed until a later phase wires the mount and the
	// host-side path resolver.
	UserDataDir string

	// ExtraReadOnlyMounts is a list of host paths to mount read-only inside the
	// sandbox at their exact host path (same-path strategy). Used for skill dirs
	// that live outside the workspace root but must be accessible for script execution.
	ExtraReadOnlyMounts []string

	// TempDirHost is the host directory mounted as /tmp inside the sandbox.
	// Empty means the backend chooses a session-local temp directory. Non-empty
	// directories are guaranteed to exist with 0700 permissions before the
	// backend sees the policy; they may be shared across sessions.
	TempDirHost string

	// ExtraWritableMounts is a list of host paths to mount writable inside the
	// sandbox at their STELLA_HOME-remapped path. Each path must live under the
	// host STELLA_HOME: the isolating backends mount it at its remapped location
	// (bwrap remaps STELLA_HOME -> /opt/stella; Seatbelt uses the host
	// path unchanged), so a path outside STELLA_HOME would bind at an unintended
	// target. This differs from ExtraReadOnlyMounts, which mounts at the exact
	// host path (same-path strategy).
	//
	// The isolating backends bind each writable (bwrap --bind, Seatbelt allow
	// file-write*); the none backend needs no mount since it shares the host
	// filesystem. The docker backend is the exception: it ships its own in-image
	// mise and never reads this field, so the caller skips populating it for
	// docker (see the backend guard in buildBasePolicy). Reaching docker with a
	// non-empty list would silently no-op — backend parity is tracked in #442.
	//
	// Used for per-user subtrees an agent must write through — today the writable
	// per-user mise home (see pkgsandbox.MiseUserToolsDir), with more to follow.
	// Each path is guaranteed to exist (the mise tree also seeded with system
	// installs) before the backend sees the policy. The policy stays mise-agnostic:
	// it only learns "these host dirs are writable", not why.
	ExtraWritableMounts []string

	// AgentSkillsDir is the host path of the admin-managed, agent-bound
	// (system_agent scope) skills dir — AgentRoot/.agents/skills. Isolating
	// backends mount it read-only at MountAgentSkills (/opt/stella/agent-skills)
	// so an agent can still load and run those skills without the host path
	// leaking. Empty for a session with no agent definition root, and ignored by
	// the none backend (host paths are valid in-sandbox there).
	AgentSkillsDir string

	// AgentPrivateDir is a legacy sibling-hiding hint, left empty in the two-root
	// layout: the agent workspace IS the per-agent dir (WorkspaceRoot), so siblings
	// are never mounted and there is nothing to hide. Still read by the macOS
	// Seatbelt profile (a deny/allow carve-out), where it stays inert while empty.
	AgentPrivateDir string
}

// NetworkPolicy defines network constraints for a sandbox session.
type NetworkPolicy struct {
	// Mode is the network access mode: disabled | allow_all.
	Mode NetworkMode

	// Timeout for network operations. Zero means no timeout.
	Timeout time.Duration
}

// Validate returns an error if the policy contains invalid configurations.
// This validates policy structure, not backend compatibility.
func (p Policy) Validate() error {
	switch p.Network.Mode {
	case NetworkDisabled, NetworkAllowAll, "":
	default:
		return fmt.Errorf("sandbox: invalid network mode %q", p.Network.Mode)
	}

	if p.Filesystem.WorkingDir == "" {
		return fmt.Errorf("sandbox: working directory is required")
	}
	if p.Filesystem.WorkspaceRoot == "" {
		p.Filesystem.WorkspaceRoot = p.Filesystem.WorkingDir
	}

	return nil
}

// NetworkModeOrDefault returns the network mode with default applied.
func (p Policy) NetworkModeOrDefault() NetworkMode {
	if p.Network.Mode == "" {
		return NetworkAllowAll
	}
	return p.Network.Mode
}

// WorkspaceRootOrDefault returns the mounted sandbox root on the host.
func (p Policy) WorkspaceRootOrDefault() string {
	if p.Filesystem.WorkspaceRoot != "" {
		return p.Filesystem.WorkspaceRoot
	}
	return p.Filesystem.WorkingDir
}

// PolicyCompatibilityError indicates a policy is not compatible with a backend.
type PolicyCompatibilityError struct {
	Backend string
	Policy  Policy
	Reason  string
}

func (e *PolicyCompatibilityError) Error() string {
	return fmt.Sprintf("sandbox: backend %q cannot satisfy policy: %s", e.Backend, e.Reason)
}

// IsPolicyCompatibilityError reports whether an error is a policy compatibility error.
func IsPolicyCompatibilityError(err error) bool {
	if err == nil {
		return false
	}
	policyCompatibilityError := &PolicyCompatibilityError{}
	return errors.As(err, &policyCompatibilityError)
}
