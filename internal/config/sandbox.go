package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

const (
	SandboxBackendAuto   = "auto"
	SandboxBackendBoxsh  = "boxsh"
	SandboxBackendDocker = "docker"
	SandboxBackendLocal  = "local"

	SandboxNetworkDisabled  = "disabled"
	SandboxNetworkAllowAll  = "allow_all"
	SandboxNetworkWhitelist = "whitelist"
)

// SandboxConfig configures the sandbox backend. The docker backend takes no
// per-agent knobs — the container image, user, mounts, and DooD translation
// are all fixed by the shipped sandbox image and auto-derived from env.
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

// localRemapOnce ensures the "local" backend remap warning is emitted at most
// once per process to avoid log spam for long-running servers.
var localRemapOnce sync.Once

// boxshRemapOnce ensures the "boxsh" backend remap warning is emitted at most
// once per process to avoid log spam for long-running servers.
var boxshRemapOnce sync.Once

// BackendName returns sandbox backend with defaults applied.
// If Backend is "local" (retired), it is remapped to "auto"
// with a one-time log warning.
// If Backend is "boxsh" (retired), it is remapped to "auto"
// with a one-time log warning.
func (c SandboxConfig) BackendName() string {
	if c.Backend == SandboxBackendLocal {
		localRemapOnce.Do(func() {
			slog.Warn("sandbox: backend \"local\" has been retired; remapping to \"auto\"")
		})
		return SandboxBackendAuto
	}
	if c.Backend == SandboxBackendBoxsh {
		boxshRemapOnce.Do(func() {
			slog.Warn("sandbox: backend \"boxsh\" has been retired; docker is the only sandbox backend; remapping to \"auto\"")
		})
		return SandboxBackendAuto
	}
	if c.Backend == "" {
		return SandboxBackendAuto
	}
	return c.Backend
}

// Validate returns an error when the sandbox configuration is invalid.
func (c SandboxConfig) Validate() error {
	switch c.BackendName() {
	case SandboxBackendAuto, SandboxBackendDocker:
		// "local" and "boxsh" both remap to "auto" in BackendName(), so they are accepted here.
	default:
		return fmt.Errorf("sandbox.backend must be one of %q or %q", SandboxBackendAuto, SandboxBackendDocker)
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
