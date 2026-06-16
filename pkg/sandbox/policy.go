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
	// (bwrap remaps STELLA_HOME -> /home/stella/.stella; Seatbelt uses the host
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

	// AgentPrivateDir is the host path of the running agent's private subdir of
	// the user home (users/{id}/agents/{agentID}), which must live under
	// WorkspaceRoot. The isolating backends redirect the agent's XDG
	// config/data/state here and hide its siblings' subdirs, so one agent cannot
	// read or tamper with another's cached credentials and bypass the per-agent
	// scoped-token isolation (#442). Caches and toolchains stay shared at the
	// user-home level; only this subtree is private. Empty when the session has
	// no sibling agents to isolate from — a user-less job, where the agent is its
	// own principal — and ignored by backends that don't enforce isolation (none,
	// and docker until #436).
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
