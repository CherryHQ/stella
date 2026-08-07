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
	// MountSystemDBSkills is the read-only mount of DB-installed system-scope
	// skills. They live in a dedicated dir (a sibling of the shipped built-ins
	// under STELLA_HOME, not mixed into it), so they get their own fixed mount.
	MountSystemDBSkills = "/opt/stella/db-skills"
	// MountBuiltinSkills is the immutable release bundle view. Isolating
	// backends expose the image/verified-revision projection here, never the
	// retired $STELLA_HOME/.agents/skills mirror.
	MountBuiltinSkills = "/opt/stella/skills/builtin"
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

// FilesystemPolicy defines the agent-visible filesystem contract for a sandbox session.
type FilesystemPolicy struct {
	// Homes preserves typed persistent identity. Providers must not derive physical
	// paths from these opaque attachments.
	Homes []HomeAttachment
	// WorkingDir is the exact agent-visible default directory for processes and
	// relative filesystem operations.
	WorkingDir string
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
	return nil
}

// NetworkModeOrDefault returns the network mode with default applied.
func (p Policy) NetworkModeOrDefault() NetworkMode {
	if p.Network.Mode == "" {
		return NetworkAllowAll
	}
	return p.Network.Mode
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
