package hostenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BuildPath returns a sanitized PATH suitable for host-execution sandbox
// backends (local, none). It prepends the stella bin directory and filters
// host PATH entries to a safe allowlist on Linux.
func BuildPath(stellaHome string) string {
	stellaBin := filepath.Join(stellaHome, "bin")
	if runtime.GOOS != "linux" {
		return prependPathEntry(stellaBin, os.Getenv("PATH"))
	}

	entries := []string{stellaBin}
	for entry := range strings.SplitSeq(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if pathAllowed(entry, stellaBin) {
			entries = append(entries, entry)
		}
	}
	entries = append(entries,
		"/run/current-system/sw/bin",
		"/usr/local/sbin",
		"/usr/local/bin",
		"/usr/sbin",
		"/usr/bin",
		"/sbin",
		"/bin",
	)
	return strings.Join(dedupeEntries(entries), string(os.PathListSeparator))
}

// BuildHome returns the HOME value for host-execution sandbox backends.
// On Linux (with bwrap), HOME is remapped to /workspace; elsewhere it
// mirrors the working directory.
func BuildHome(workDir string) string {
	if runtime.GOOS == "linux" {
		return "/workspace"
	}
	return workDir
}

// CopyHostEnv copies a fixed allowlist of host environment variables into env.
// Only locale, terminal, and proxy variables are included.
func CopyHostEnv(env map[string]string) {
	for _, key := range []string{
		"TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
	} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
}

// PathAllowed reports whether a PATH entry is in the safe allowlist.
// Exported for testing.
func PathAllowed(entry, stellaBin string) bool {
	return pathAllowed(entry, stellaBin)
}

func pathAllowed(entry, stellaBin string) bool {
	if entry == "" {
		return false
	}
	if stellaBin != "" && entry == stellaBin {
		return true
	}
	for _, root := range []string{"/usr", "/bin", "/sbin", "/nix", "/run/current-system/sw"} {
		if entry == root || strings.HasPrefix(entry, root+"/") {
			return true
		}
	}
	return false
}

func prependPathEntry(entry, existing string) string {
	if entry == "" {
		return existing
	}
	if existing == "" {
		return entry
	}
	return entry + string(os.PathListSeparator) + existing
}

func dedupeEntries(entries []string) []string {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}
