package config

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	stellaHomeOnce sync.Once
	stellaHomeVal  string
)

// StellaHome returns the stella home directory.
// Priority: STELLA_HOME env -> ~/.stella
// The result is cached after the first call.
func StellaHome() string {
	stellaHomeOnce.Do(func() {
		stellaHomeVal = resolveStellaHome()
	})
	return stellaHomeVal
}

func resolveStellaHome() string {
	if v := os.Getenv("STELLA_HOME"); v != "" {
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
		return filepath.Join(".", ".stella")
	}
	return filepath.Join(home, ".stella")
}

// ResetStellaHome clears the cached StellaHome value (for testing).
func ResetStellaHome() {
	stellaHomeOnce = sync.Once{}
	stellaHomeVal = ""
}

// CachePath returns the cache directory inside the stella home.
func CachePath() string {
	return filepath.Join(StellaHome(), "cache")
}

// DBPath returns the default database path inside the stella home.
func DBPath() string {
	return filepath.Join(StellaHome(), "stella.db")
}

// ServerURL returns the URL CLI commands should use to talk to the local
// stella server. Priority: STELLA_SERVER_URL env -> http://127.0.0.1:25678
// (the default admin port from cmd/stella/gateway.go).
func ServerURL() string {
	if v := os.Getenv("STELLA_SERVER_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:25678"
}
