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

// MountAccess is the access mode a sandbox mount grants inside the sandbox.
type MountAccess uint8

const (
	// MountReadOnly allows reads but rejects writes through host-side tools and
	// asks isolating backends for a read-only bind when the platform supports it.
	MountReadOnly MountAccess = iota
	// MountReadWrite allows reads and writes.
	MountReadWrite
)

// Mount declares one process-visible data root authorized through Session.Files.
// Provider-private physical backing paths deliberately do not belong to Policy.
type Mount struct {
	SandboxPath string
	Access      MountAccess
}

// FilesystemPolicy defines filesystem constraints for a sandbox session.
type FilesystemPolicy struct {
	// WorkingDir is the process-visible POSIX working directory.
	WorkingDir string

	// Mounts contains the authorized data roots in the active process view.
	Mounts []Mount
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
