package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
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

// miseUserKeyPattern restricts a per-user mise directory name to a single safe
// path component. Anything else falls back to the shared (read-only) system tree.
var miseUserKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// MiseUserToolsDir returns the per-principal writable MISE_DATA_DIR. It mirrors a
// real machine's per-user mise home: each principal gets one tree shared by all
// their agents, layered above the shared read-only system installs. principalDir
// is the home subtree — always "users", the only top-level isolation boundary
// (#442) — and id is the principal's key: a raw user ID, or a channel group's ID
// under a "group-" prefix so equal raw IDs across users and groups can never
// collide. Keying as {stellaHome}/{principalDir}/{id}/.mise-tools never depends on
// the agent, so all of a principal's agents share one tree. An empty or unsafe
// argument yields "" so callers fall back to the system tree.
func MiseUserToolsDir(stellaHome, principalDir, id string) string {
	if principalDir != "users" {
		return ""
	}
	if !miseUserKeyPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(stellaHome, principalDir, id, ".mise-tools")
}

// MiseUserShimsDir returns the per-user mise shims directory. It is prepended to
// PATH ahead of the system shims so a user's own tool versions win.
func MiseUserShimsDir(userToolsDir string) string {
	return filepath.Join(userToolsDir, "shims")
}

// PerUserMiseDataDir returns the per-user MISE_DATA_DIR carried in a session's
// env, or "" when it points at the shared read-only system tree (or is unset).
// It lets a host backend recover the per-user mise home — to derive the shims
// dir for PATH — from the env it already remaps, so the FilesystemPolicy needs no
// mise-specific field. stellaHome is the host STELLA_HOME used to recognize the
// system tree; the returned path is whatever scope the env holds (host path here,
// the backend remaps it as needed).
//
// Precondition: env's MISE_DATA_DIR and stellaHome must be in the same scope
// (both host paths, or both sandbox paths). Callers that remap MISE_* env vars
// must read this before remapping (see adjustPolicy), since after remap the
// data dir no longer matches the host system tree this compares against.
func PerUserMiseDataDir(env map[string]string, stellaHome string) string {
	dir := env["MISE_DATA_DIR"]
	if dir == "" || dir == MiseToolsDir(stellaHome) {
		return ""
	}
	return dir
}

// HostEnvBuildPath returns a sanitized PATH suitable for host-execution sandbox
// backends (local, none). It prepends the per-user mise shims (so a user's own
// tool versions win), then Stella's embedded runtimes and the system mise shims, and
// filters host PATH entries to a safe allowlist on Linux. An empty userShimsDir
// is dropped, so callers without a per-user tree pass "".
func HostEnvBuildPath(stellaHome, userShimsDir string) string {
	stellaBin := filepath.Join(stellaHome, "bin")
	shimsDir := MiseShimsDir(stellaHome)
	if runtime.GOOS != "linux" {
		return strings.Join(hostEnvDedupeEntries([]string{
			userShimsDir, stellaBin, shimsDir, os.Getenv("PATH"),
		}), string(os.PathListSeparator))
	}

	entries := []string{userShimsDir, stellaBin, shimsDir}
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
