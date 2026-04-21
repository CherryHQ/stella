package sandbox

import sandboxpkg "github.com/vaayne/anna/pkg/sandbox"

type (
	Policy                   = sandboxpkg.Policy
	FilesystemPolicy         = sandboxpkg.FilesystemPolicy
	NetworkMode              = sandboxpkg.NetworkMode
	NetworkPolicy            = sandboxpkg.NetworkPolicy
	PolicyCompatibilityError = sandboxpkg.PolicyCompatibilityError
)

const (
	NetworkDisabled = sandboxpkg.NetworkDisabled
	NetworkAllowAll = sandboxpkg.NetworkAllowAll
)

func IsPolicyCompatibilityError(err error) bool {
	return sandboxpkg.IsPolicyCompatibilityError(err)
}
