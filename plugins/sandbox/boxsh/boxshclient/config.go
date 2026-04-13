package boxshclient

import (
	"os"
	"path/filepath"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

const (
	NetworkDisabled  = string(sandboxpkg.NetworkDisabled)
	NetworkAllowAll  = string(sandboxpkg.NetworkAllowAll)
	NetworkWhitelist = string(sandboxpkg.NetworkWhitelist)
)

type NetworkConfig struct {
	Mode      string
	Allowlist []string
}

func (c NetworkConfig) ModeOrDefault() string {
	if c.Mode == "" {
		return NetworkDisabled
	}
	return c.Mode
}

func DefaultAnnaHome() string {
	if v := os.Getenv("ANNA_HOME"); v != "" {
		if filepath.IsAbs(v) {
			return v
		}
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".anna")
	}
	return filepath.Join(home, ".anna")
}
