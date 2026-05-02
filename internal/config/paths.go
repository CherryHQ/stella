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

// CachePath returns the cache directory inside the anna home.
func CachePath() string {
	return filepath.Join(AnnaHome(), "cache")
}

// DBPath returns the default database path inside the anna home.
func DBPath() string {
	return filepath.Join(AnnaHome(), "anna.db")
}

// ServerURL returns the URL CLI commands should use to talk to the local
// anna server. Priority: ANNA_SERVER_URL env -> http://127.0.0.1:25678
// (the default admin port from cmd/anna/gateway.go).
func ServerURL() string {
	if v := os.Getenv("ANNA_SERVER_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:25678"
}
