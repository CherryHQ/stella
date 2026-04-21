package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
)

const (
	SandboxBackendDocker = "docker"

	SandboxNetworkDisabled = "disabled"
	SandboxNetworkAllowAll = "allow_all"
)

// sandboxBackendIgnoreOnce ensures the deprecated backend key warning is emitted
// at most once per process to avoid log spam for long-running servers.
var sandboxBackendIgnoreOnce sync.Once

// SandboxConfig configures the sandbox backend. The docker backend takes no
// per-agent knobs — the container image, user, mounts, and DooD translation
// are all fixed by the shipped sandbox image and auto-derived from env.
type SandboxConfig struct {
	Network SandboxNetworkConfig `json:"network"`
}

// UnmarshalJSON implements json.Unmarshaler so that legacy "backend" keys in
// stored or user-supplied config are silently ignored with a one-time log
// warning instead of producing an unknown-field error.
func (c *SandboxConfig) UnmarshalJSON(data []byte) error {
	// Probe for the deprecated "backend" key.
	var probe struct {
		Backend *string              `json:"backend"`
		Network SandboxNetworkConfig `json:"network"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.Backend != nil {
		sandboxBackendIgnoreOnce.Do(func() {
			slog.Warn("sandbox: config key \"backend\" is deprecated and ignored; docker is the only supported backend")
		})
	}
	c.Network = probe.Network
	return nil
}

// SandboxNetworkConfig configures sandbox network access.
type SandboxNetworkConfig struct {
	Mode      string   `json:"mode"`
	Allowlist []string `json:"allowlist"`
}

// NetworkMode returns the configured network mode with defaults applied.
func (c SandboxConfig) NetworkMode() string {
	if c.Network.Mode == "" {
		return SandboxNetworkDisabled
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
