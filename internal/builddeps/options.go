package builddeps

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Config describes one pre-build third-party dependency sync run.
type Config struct {
	WorkDir string

	SyncSkills bool
	SyncTools  bool

	GOOS   string
	GOARCH string

	LarkRef string
}

// Normalized returns a copy with runtime defaults filled in.
func (c Config) Normalized() Config {
	if c.WorkDir == "" {
		c.WorkDir = "."
	}
	c.WorkDir = filepath.Clean(c.WorkDir)
	if c.GOOS == "" {
		c.GOOS = runtime.GOOS
	}
	if c.GOARCH == "" {
		c.GOARCH = runtime.GOARCH
	}
	c.LarkRef = strings.TrimSpace(c.LarkRef)
	return c
}

// Validate checks that the sync request is internally consistent.
func (c Config) Validate() error {
	if !c.SyncSkills && !c.SyncTools {
		return fmt.Errorf("at least one sync mode must be selected (skills or tools)")
	}
	if c.SyncTools {
		switch c.GOOS {
		case "darwin", "linux", "windows":
		default:
			return fmt.Errorf("unsupported goos %q", c.GOOS)
		}
		switch c.GOARCH {
		case "amd64", "arm64":
		default:
			return fmt.Errorf("unsupported goarch %q", c.GOARCH)
		}
	}
	return nil
}

// Platform returns the target platform key used by embedded binaries.
func (c Config) Platform() string {
	return c.GOOS + "-" + c.GOARCH
}
