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
		return NetworkDisabled
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
