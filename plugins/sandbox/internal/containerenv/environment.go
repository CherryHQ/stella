// Package containerenv renders declared environment values for Linux containers.
package containerenv

import (
	"path/filepath"
	"strings"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

type valueKind uint8

const (
	valueLiteral valueKind = iota
	valueHostPath
	valueHostPathList
	valueDrop
)

// valueKinds is the schema for runner-owned environment values that need a
// container coordinate conversion. Unknown variables are literals, even when their
// values look like absolute paths; Vault, OAuth, and plugin values must not
// acquire filesystem semantics from their shape.
var valueKinds = map[string]valueKind{
	sandboxpkg.EnvHome:            valueHostPath,
	sandboxpkg.EnvStellaAssetsDir: valueHostPath,
	sandboxpkg.EnvTempDir:         valueHostPath,
	sandboxpkg.EnvXDGConfigHome:   valueHostPath,
	sandboxpkg.EnvXDGDataHome:     valueHostPath,
	sandboxpkg.EnvXDGStateHome:    valueHostPath,
	sandboxpkg.EnvXDGCacheHome:    valueHostPath,
	"STELLA_HOME":                 valueHostPath,
	"BASH_ENV":                    valueHostPath,
	"MISE_DATA_DIR":               valueHostPath,
	"MISE_CONFIG_DIR":             valueHostPath,
	"MISE_CACHE_DIR":              valueHostPath,
	"MISE_STATE_DIR":              valueHostPath,
	"MISE_SYSTEM_CONFIG_FILE":     valueHostPath,
	"MISE_GLOBAL_CONFIG_FILE":     valueHostPath,
	"MISE_TRUSTED_CONFIG_PATHS":   valueHostPathList,
	// Host PATH may contain host-platform binaries and must never override the
	// image PATH. WithToolPaths adds container-native tool directories later.
	"PATH":                   valueDrop,
	sandboxpkg.EnvRunnerPath: valueDrop,
	"STELLA_USER_DIR":        valueDrop,
}

// Translate renders the declared path-valued entries into container
// coordinates. Declared paths without a mount/env mapping fail closed by being
// omitted; literals pass through unchanged. The caller supplies its authorized
// coordinate mapping.
func Translate(env map[string]string, translate func(string) (string, bool)) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		switch valueKinds[key] {
		case valueDrop:
			continue
		case valueHostPath:
			if translated, ok := translate(value); ok {
				out[key] = translated
			}
		case valueHostPathList:
			if translated := translateList(value, translate); translated != "" {
				out[key] = translated
			}
		default:
			out[key] = value
		}
	}
	return out
}

func translateList(value string, translate func(string) (string, bool)) string {
	seen := map[string]struct{}{}
	var translated []string
	for entry := range strings.SplitSeq(value, string(filepath.ListSeparator)) {
		path, ok := translate(entry)
		if !ok {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		translated = append(translated, path)
	}
	// The target container is always Linux, independent of the host separator.
	return strings.Join(translated, ":")
}

// DefaultPATH is the image-baked PATH from the Dockerfile ENV directive.
// It is used as the base when building a container exec PATH that prepends
// container-native user tool cache paths. Keep in sync with the ENV PATH line
// in plugins/sandbox/docker/Dockerfile.
const DefaultPATH = "/opt/stella/bin:/opt/stella/.mise-tools/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// WithToolPaths prepends container-native tool directories to PATH (the
// per-user mise shims so an agent's own installs win, then any manifest tool
// cache). Built-in tools resolve through the image-baked PATH (the shared
// /opt/stella mise tree); the host filesystem is never used for container
// executable resolution because it may contain host-platform binaries.
func WithToolPaths(env map[string]string, toolBinPaths []string) map[string]string {
	base := env["PATH"]
	if base == "" {
		base = DefaultPATH
	}
	entries := append([]string(nil), toolBinPaths...)
	entries = append(entries, base)
	env["PATH"] = strings.Join(entries, ":")
	// Snapshot the final container-native PATH after per-call overrides are
	// merged, so no ambient or per-call value can impersonate the runner copy.
	env[sandboxpkg.EnvRunnerPath] = env["PATH"]
	return env
}
