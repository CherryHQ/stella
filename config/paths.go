package config

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	annaHomeOnce sync.Once
	annaHomeVal  string
)

// AnnaHome returns the anna home directory.
// Priority: ANNA_HOME env -> ~/.anna
// The result is cached after the first call.
func AnnaHome() string {
	annaHomeOnce.Do(func() {
		annaHomeVal = resolveAnnaHome()
	})
	return annaHomeVal
}

func resolveAnnaHome() string {
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

// ResetAnnaHome clears the cached AnnaHome value (for testing).
func ResetAnnaHome() {
	annaHomeOnce = sync.Once{}
	annaHomeVal = ""
}

// Path returns the path to config.yaml inside the anna home.
func Path() string {
	return filepath.Join(AnnaHome(), "config.yaml")
}

// CachePath returns the cache directory inside the anna home.
func CachePath() string {
	return filepath.Join(AnnaHome(), "cache")
}

// Workspace path helpers — all data lives under Workspace.

func (cfg *Config) StatePath() string {
	return filepath.Join(cfg.Workspace, "state.yaml")
}

func (cfg *Config) SessionsPath() string {
	return filepath.Join(cfg.Workspace, "sessions")
}

func (cfg *Config) SkillsPath() string {
	return filepath.Join(cfg.Workspace, "skills")
}

func (cfg *Config) ModelsPath() string {
	return filepath.Join(CachePath(), "models.json")
}

func (cfg *Config) LogPath() string {
	return filepath.Join(cfg.Workspace, "anna.log")
}
