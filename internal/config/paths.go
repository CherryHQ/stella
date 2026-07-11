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

// DBPath returns the path to the legacy SQLite database inside the stella home.
// It is retained for the one-time SQLite→PostgreSQL data-migration tool; the
// running server uses ServerConfig.Database.URL instead.
func DBPath() string {
	return filepath.Join(StellaHome(), "stella.db")
}
