package sandbox

import sandboxpkg "github.com/vaayne/anna/pkg/sandbox"

type (
	Policy                   = sandboxpkg.Policy
	FilesystemPolicy         = sandboxpkg.FilesystemPolicy
	NetworkMode              = sandboxpkg.NetworkMode
	NetworkPolicy            = sandboxpkg.NetworkPolicy
	ProcessPolicy            = sandboxpkg.ProcessPolicy
	PolicyCompatibilityError = sandboxpkg.PolicyCompatibilityError
)

const (
	NetworkDisabled  = sandboxpkg.NetworkDisabled
	NetworkAllowAll  = sandboxpkg.NetworkAllowAll
	NetworkWhitelist = sandboxpkg.NetworkWhitelist
)

func IsPolicyCompatibilityError(err error) bool {
	return sandboxpkg.IsPolicyCompatibilityError(err)
}
