package docker

import (
	"maps"
	"path"
	"path/filepath"
	"strings"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

func withServerURL(env map[string]string, url string) map[string]string {
	if url == "" {
		return env
	}
	out := maps.Clone(env)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out["STELLA_SERVER_URL"] = url
	return out
}

func mergeEnv(policyEnv, optsEnv map[string]string) map[string]string {
	out := make(map[string]string, len(policyEnv)+len(optsEnv))
	maps.Copy(out, policyEnv)
	maps.Copy(out, optsEnv)
	return out
}

type dockerEnvKind uint8

const (
	dockerEnvLiteral dockerEnvKind = iota
	dockerEnvHostPath
	dockerEnvHostPathList
	dockerEnvDrop
)

// dockerEnvKinds is the schema for runner-owned environment values that need a
// Docker coordinate conversion. Unknown variables are literals, even when their
// values look like absolute paths; Vault, OAuth, and plugin values must not
// acquire filesystem semantics from their shape.
var dockerEnvKinds = map[string]dockerEnvKind{
	sandboxpkg.EnvHome:            dockerEnvHostPath,
	sandboxpkg.EnvStellaAssetsDir: dockerEnvHostPath,
	sandboxpkg.EnvTempDir:         dockerEnvHostPath,
	sandboxpkg.EnvXDGConfigHome:   dockerEnvHostPath,
	sandboxpkg.EnvXDGDataHome:     dockerEnvHostPath,
	sandboxpkg.EnvXDGStateHome:    dockerEnvHostPath,
	sandboxpkg.EnvXDGCacheHome:    dockerEnvHostPath,
	"STELLA_HOME":                 dockerEnvHostPath,
	"LARKSUITE_CLI_CONFIG_DIR":    dockerEnvHostPath,
	"LARKSUITE_CLI_DATA_DIR":      dockerEnvHostPath,
	"MISE_DATA_DIR":               dockerEnvHostPath,
	"MISE_CONFIG_DIR":             dockerEnvHostPath,
	"MISE_CACHE_DIR":              dockerEnvHostPath,
	"MISE_STATE_DIR":              dockerEnvHostPath,
	"MISE_GLOBAL_CONFIG_FILE":     dockerEnvHostPath,
	"MISE_TRUSTED_CONFIG_PATHS":   dockerEnvHostPathList,
	// Host PATH may contain host-platform binaries and must never override the
	// image PATH. injectToolPaths adds container-native tool directories later.
	"PATH":            dockerEnvDrop,
	"STELLA_USER_DIR": dockerEnvDrop,
}

// translateEnvPaths renders the declared path-valued entries into container
// coordinates. Declared paths without a mount/env mapping fail closed by being
// omitted; literals pass through unchanged. envMaps covers path prefixes such
// as STELLA_HOME that intentionally are not exposed as a general mount.
func translateEnvPaths(env map[string]string, mountTable []dockerclient.Mount, envMaps []envPathMap) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		switch dockerEnvKinds[key] {
		case dockerEnvDrop:
			continue
		case dockerEnvHostPath:
			if translated, ok := translateDeclaredEnvPath(value, mountTable, envMaps); ok {
				out[key] = translated
			}
		case dockerEnvHostPathList:
			if translated := translateDeclaredEnvPathList(value, mountTable, envMaps); translated != "" {
				out[key] = translated
			}
		default:
			out[key] = value
		}
	}
	return out
}

func translateDeclaredEnvPathList(value string, mountTable []dockerclient.Mount, envMaps []envPathMap) string {
	seen := map[string]struct{}{}
	var translated []string
	for entry := range strings.SplitSeq(value, string(filepath.ListSeparator)) {
		path, ok := translateDeclaredEnvPath(entry, mountTable, envMaps)
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

func translateDeclaredEnvPath(value string, mountTable []dockerclient.Mount, envMaps []envPathMap) (string, bool) {
	if container, err := toContainerPath(mountTable, value); err == nil {
		return container, true
	}
	if mapped, ok := applyEnvPathMaps(envMaps, value); ok {
		return mapped, true
	}
	if isContainerPath(mountTable, value) {
		return cleanContainerPath(value), true
	}
	return "", false
}

// isContainerPath reports whether v already names a path inside the container
// (equal to or under a mount's container path), so it needs no translation.
func isContainerPath(mountTable []dockerclient.Mount, v string) bool {
	v = cleanContainerPath(v)
	for _, m := range mountTable {
		containerPath := cleanContainerPath(m.ContainerPath)
		if containerPath == "." {
			continue
		}
		if v == containerPath || strings.HasPrefix(v, containerPath+"/") {
			return true
		}
	}
	return false
}

func applyEnvPathMaps(maps []envPathMap, hostPath string) (string, bool) {
	for _, m := range maps {
		rel, err := filepath.Rel(m.HostPrefix, hostPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." {
			return cleanContainerPath(m.ContainerPrefix), true
		}
		return path.Join(cleanContainerPath(m.ContainerPrefix), strings.ReplaceAll(rel, "\\", "/")), true
	}
	return "", false
}

// containerDefaultPATH is the image-baked PATH from the Dockerfile ENV directive.
// It is used as the base when building a container exec PATH that prepends
// container-native user tool cache paths. Keep in sync with the ENV PATH line
// in plugins/sandbox/docker/Dockerfile.
const containerDefaultPATH = "/opt/stella/.mise-tools/shims:/opt/stella/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// injectToolPaths prepends container-native tool directories to PATH (the
// per-user mise shims so an agent's own installs win, then any manifest tool
// cache). Built-in tools resolve through the image-baked PATH (the shared
// /opt/stella mise tree); the host filesystem is never used for docker
// executable resolution because it may contain host-platform binaries.
func injectToolPaths(env map[string]string, toolBinPaths []string) map[string]string {
	if len(toolBinPaths) == 0 {
		return env
	}
	base := env["PATH"]
	if base == "" {
		base = containerDefaultPATH
	}
	entries := append([]string(nil), toolBinPaths...)
	entries = append(entries, base)
	env["PATH"] = strings.Join(entries, ":")
	return env
}

// envPathMap is an extra host→container path translation that translateEnvPaths
// applies before consulting the mount table. Used for STELLA_HOME which needs
// env translation but must NOT be in the mount table (that would allow file
// reads across the entire directory).
type envPathMap struct {
	HostPrefix      string
	ContainerPrefix string
}
