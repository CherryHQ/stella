package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StellaHomeSandboxDirs returns the subdirectory names (relative to STELLA_HOME)
// that sandbox backends must expose. Every backend — bwrap, Docker, none — should
// derive its mount list from this slice so adding a new directory is a one-line
// change. The returned names use filepath.Separator and are safe to join with
// any absolute root.
func StellaHomeSandboxDirs() []string {
	return []string{
		"bin",
		".mise-tools",
		filepath.Join(".agents", "skills"),
	}
}

// MiseToolsDir returns the root MISE_DATA_DIR for Stella-managed mise installs.
// This is the single source of truth for the on-disk layout: the manifest/org
// reconcilers install into it and the sandbox PATH is built from it, so both
// sides must derive their paths from here to stay in lockstep.
func MiseToolsDir(stellaHome string) string {
	return filepath.Join(stellaHome, ".mise-tools")
}

// MiseShimsDir returns the mise shims directory for host-execution sandbox
// backends. Tools installed by the manifest/org reconcilers are exposed here as
// shims (not copied into bin), so it must be on PATH for them to resolve.
func MiseShimsDir(stellaHome string) string {
	return filepath.Join(MiseToolsDir(stellaHome), "shims")
}

// HostEnvBuildPath returns a sanitized PATH suitable for host-execution sandbox
// backends (local, none). It prepends the mise shims and stella bin directories
// and filters host PATH entries to a safe allowlist on Linux.
func HostEnvBuildPath(stellaHome string) string {
	stellaBin := filepath.Join(stellaHome, "bin")
	shimsDir := MiseShimsDir(stellaHome)
	if runtime.GOOS != "linux" {
		return strings.Join(hostEnvDedupeEntries([]string{
			shimsDir, stellaBin, os.Getenv("PATH"),
		}), string(os.PathListSeparator))
	}

	entries := []string{shimsDir, stellaBin}
	for entry := range strings.SplitSeq(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if hostEnvPathAllowed(entry, stellaBin) {
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
	return strings.Join(hostEnvDedupeEntries(entries), string(os.PathListSeparator))
}

// HostEnvBuildHome returns the HOME value for host-execution sandbox backends.
// On Linux (with bwrap), HOME is remapped to /workspace; elsewhere it
// mirrors the working directory.
func HostEnvBuildHome(workDir string) string {
	if runtime.GOOS == "linux" {
		return "/workspace"
	}
	return workDir
}

// HostEnvCopy copies a fixed allowlist of host environment variables into env.
// Only locale, terminal, and proxy variables are included.
func HostEnvCopy(env map[string]string) {
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

// HostEnvPathAllowed reports whether a PATH entry is in the safe allowlist.
func HostEnvPathAllowed(entry, stellaBin string) bool {
	return hostEnvPathAllowed(entry, stellaBin)
}

func hostEnvPathAllowed(entry, stellaBin string) bool {
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

func hostEnvDedupeEntries(entries []string) []string {
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
