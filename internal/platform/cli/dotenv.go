package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv reads $STELLA_HOME/.env and sets any key that is not already
// present in the environment. Existing OS/service-injected variables win.
// Missing file is silently ignored; parse errors are skipped per line.
func LoadDotEnv() {
	stellaHome := os.Getenv("STELLA_HOME")
	if stellaHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		stellaHome = filepath.Join(home, ".stella")
	}

	f, err := os.Open(filepath.Join(stellaHome, ".env"))
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if k == "" {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		// Presence, not emptiness, decides precedence: an OS/service-injected
		// variable explicitly set to "" still wins over the .env file, matching
		// "Existing OS/service-injected variables win" above. LookupEnv
		// distinguishes unset from empty; Getenv cannot.
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, v)
		}
	}
}
