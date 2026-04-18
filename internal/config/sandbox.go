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
	SandboxBackendAuto   = "auto"
	SandboxBackendBoxsh  = "boxsh"
	SandboxBackendDocker = "docker"
	SandboxBackendLocal  = "local"

	SandboxNetworkDisabled  = "disabled"
	SandboxNetworkAllowAll  = "allow_all"
	SandboxNetworkWhitelist = "whitelist"
)

// SandboxConfig configures the sandbox backend.
type SandboxConfig struct {
	Backend string               `json:"backend"`
	Docker  SandboxDockerConfig  `json:"docker"`
	Network SandboxNetworkConfig `json:"network"`
}

// SandboxDockerConfig configures the docker sandbox backend.
type SandboxDockerConfig struct {
	Image       string   `json:"image"`        // e.g. "alpine:3.20". Empty = backend default.
	User        string   `json:"user"`         // "uid:gid" string. Empty = default uid/gid from os/user.
	AllowPull   bool     `json:"allow_pull"`   // whether preflight may run `docker pull`. Default false.
	ExtraMounts []string `json:"extra_mounts"` // extra bind mounts. Each entry: "host:container[:ro]".
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
		return SandboxBackendAuto
	}
	return c.Backend
}

// Validate returns an error when the docker sandbox configuration is invalid.
func (c SandboxDockerConfig) Validate() error {
	if img := strings.TrimSpace(c.Image); c.Image != "" {
		if img == "" || strings.IndexFunc(img, unicode.IsSpace) >= 0 {
			return fmt.Errorf("sandbox.docker.image must be a non-empty reference without whitespace")
		}
	}
	if c.User != "" && !dockerUserPattern.MatchString(c.User) {
		return fmt.Errorf("sandbox.docker.user must match uid[:gid] where uid and gid are non-negative integers")
	}
	for _, m := range c.ExtraMounts {
		parts := strings.Split(m, ":")
		switch len(parts) {
		case 2:
			if !strings.HasPrefix(parts[0], "/") || !strings.HasPrefix(parts[1], "/") {
				return fmt.Errorf("sandbox.docker.extra_mounts entry %q: host and container paths must be absolute", m)
			}
		case 3:
			if !strings.HasPrefix(parts[0], "/") || !strings.HasPrefix(parts[1], "/") {
				return fmt.Errorf("sandbox.docker.extra_mounts entry %q: host and container paths must be absolute", m)
			}
			if parts[2] != "ro" {
				return fmt.Errorf("sandbox.docker.extra_mounts entry %q: optional third part must be %q", m, "ro")
			}
		default:
			return fmt.Errorf("sandbox.docker.extra_mounts entry %q: must be host:container or host:container:ro", m)
		}
	}
	return nil
}

var dockerUserPattern = regexp.MustCompile(`^\d+(:\d+)?$`)

// Validate returns an error when the sandbox configuration is invalid.
func (c SandboxConfig) Validate() error {
	switch c.BackendName() {
	case SandboxBackendAuto, SandboxBackendBoxsh, SandboxBackendDocker, SandboxBackendLocal:
	default:
		return fmt.Errorf("sandbox.backend must be one of %q, %q, %q, or %q", SandboxBackendAuto, SandboxBackendBoxsh, SandboxBackendDocker, SandboxBackendLocal)
	}

	if err := c.Docker.Validate(); err != nil {
		return err
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
