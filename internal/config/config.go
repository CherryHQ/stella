package config

import (
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// RunnerConfig configures the agent runner.
type RunnerConfig struct {
	Type        string           `json:"type"`
	System      string           `json:"system"`
	IdleTimeout int              `json:"idle_timeout"`
	Compaction  CompactionConfig `json:"compaction"`
}

// CompactionConfig controls automatic session compaction.
type CompactionConfig struct {
	// MaxTokens triggers compaction when the estimated token count exceeds this.
	// 0 (or omitted) uses the default of 80000. Negative values disable
	// automatic compaction. Manual /compact still works.
	MaxTokens int `json:"max_tokens"`
	// KeepTail is the number of recent message entries to preserve verbatim
	// after compaction. Default: 20.
	KeepTail int `json:"keep_tail"`
}

const (
	SandboxNetworkDisabled  = "disabled"
	SandboxNetworkAllowAll  = "allow_all"
	SandboxNetworkWhitelist = "whitelist"
)

// SandboxConfig configures the sandbox backend.
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

// SchedulerConfig configures the scheduler subsystem.
type SchedulerConfig struct {
	Enabled *bool  `json:"enabled"`
	DataDir string `json:"data_dir"`
}

// IsEnabled returns whether the scheduler is enabled (defaults to true).
func (c SchedulerConfig) IsEnabled() bool {
	return boolDefault(c.Enabled, true)
}

// HeartbeatConfig configures periodic heartbeat checks.
type HeartbeatConfig struct {
	Enabled *bool  `json:"enabled"`
	Every   string `json:"every"`
	File    string `json:"file"`
}

// IsEnabled returns whether heartbeat is enabled (defaults to false).
func (c HeartbeatConfig) IsEnabled() bool {
	return boolDefault(c.Enabled, false)
}

// Interval returns the configured heartbeat cadence.
func (c HeartbeatConfig) Interval() string {
	if c.Every == "" {
		return "10m"
	}
	return c.Every
}

// FilePath resolves the configured heartbeat file relative to the workspace.
func (c HeartbeatConfig) FilePath(workspace string) string {
	if c.File == "" {
		return filepath.Join(workspace, "HEARTBEAT.md")
	}
	if filepath.IsAbs(c.File) {
		return c.File
	}
	return filepath.Join(workspace, c.File)
}

// boolDefault dereferences a *bool pointer, returning def if the pointer is nil.
func boolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
