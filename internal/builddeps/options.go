package builddeps

import (
	"fmt"
	"path/filepath"
	"runtime"
)

// Config describes one pre-build third-party dependency sync run.
type Config struct {
	WorkDir string

	SyncTools bool

	GOOS   string
	GOARCH string
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
	return c
}

// Validate checks that the sync request is internally consistent.
func (c Config) Validate() error {
	if !c.SyncTools {
		return fmt.Errorf("sync mode must be selected (tools)")
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
