package config

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
)

const (
	SandboxNetworkDisabled  = "disabled"
	SandboxNetworkAllowAll  = "allow_all"
	SandboxNetworkWhitelist = "whitelist"
)

// SandboxConfig configures the sandbox backend.
type SandboxConfig struct {
	Backend string               `json:"backend"`
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
		return SandboxNetworkDisabled
	}
	return c.Network.Mode
}

// BackendName returns sandbox backend with defaults applied.
func (c SandboxConfig) BackendName() string {
	if c.Backend == "" {
		return "auto"
	}
	return c.Backend
}

// Validate returns an error when the sandbox configuration is invalid.
func (c SandboxConfig) Validate() error {
	switch c.BackendName() {
	case "auto", "boxsh":
	default:
		return fmt.Errorf("sandbox.backend must be one of %q or %q", "auto", "boxsh")
	}

	switch mode := c.NetworkMode(); mode {
	case SandboxNetworkDisabled, SandboxNetworkAllowAll:
		if len(c.Network.Allowlist) > 0 {
			return fmt.Errorf("sandbox.network.allowlist requires whitelist mode")
		}
		return nil
	case SandboxNetworkWhitelist:
		if len(c.Network.Allowlist) == 0 {
			return fmt.Errorf("sandbox.network.allowlist is required when mode is whitelist")
		}
		for _, entry := range c.Network.Allowlist {
			if err := validateSandboxAllowlistEntry(entry); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("sandbox.network.mode must be one of %q, %q, or %q", SandboxNetworkDisabled, SandboxNetworkAllowAll, SandboxNetworkWhitelist)
	}
}

func validateSandboxAllowlistEntry(entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return fmt.Errorf("sandbox.network.allowlist entries must not be empty")
	}
	if _, err := netip.ParsePrefix(entry); err == nil {
		return nil
	}
	if net.ParseIP(entry) != nil {
		return nil
	}
	if strings.IndexFunc(entry, func(r rune) bool {
		return unicode.IsSpace(r) || r == '/' || r == ':' || r == '\\'
	}) >= 0 {
		return fmt.Errorf("sandbox.network.allowlist entry %q must be an IP, CIDR, or hostname", entry)
	}
	if !hostnamePattern.MatchString(entry) {
		return fmt.Errorf("sandbox.network.allowlist entry %q must be an IP, CIDR, or hostname", entry)
	}
	return nil
}

var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
