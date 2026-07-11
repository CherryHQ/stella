package config

import (
	"fmt"
)

const (
	SandboxBackendDocker = "docker"
	SandboxBackendLocal  = "local"
	SandboxBackendNone   = "none"

	SandboxNetworkDisabled = "disabled"
	SandboxNetworkAllowAll = "allow_all"
)

// SandboxConfig configures the sandbox backend.
// The active backend is selected globally on the Plugins page.
type SandboxConfig struct {
	Network SandboxNetworkConfig `json:"network"`
}

// SandboxNetworkConfig configures sandbox network access.
type SandboxNetworkConfig struct {
	Mode      string   `json:"mode"`
	Allowlist []string `json:"allowlist"`
}

// NetworkMode returns the configured network mode with defaults applied.
func (c SandboxConfig) NetworkMode() string {
	if c.Network.Mode == "" {
		return SandboxNetworkAllowAll
	}
	return c.Network.Mode
}

// Validate returns an error when the sandbox configuration is invalid.
func (c SandboxConfig) Validate() error {
	switch mode := c.NetworkMode(); mode {
	case SandboxNetworkDisabled, SandboxNetworkAllowAll:
		if len(c.Network.Allowlist) > 0 {
			return fmt.Errorf("sandbox.network.allowlist requires whitelist mode")
		}
		return nil
	default:
		return fmt.Errorf("sandbox.network.mode must be one of %q or %q", SandboxNetworkDisabled, SandboxNetworkAllowAll)
	}
}
