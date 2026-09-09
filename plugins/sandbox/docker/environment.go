package docker

import (
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/containerenv"
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

func envKeys(env map[string]string) []string {
	return slices.Sorted(maps.Keys(env))
}

func unsetEnvKeys(creationKeys []string, env map[string]string, mode sandboxpkg.EnvMode) []string {
	if mode != sandboxpkg.EnvReplace {
		return nil
	}
	unset := make([]string, 0)
	for _, key := range creationKeys {
		if _, present := env[key]; !present {
			unset = append(unset, key)
		}
	}
	return unset
}

func translateEnvPaths(env map[string]string, mountTable []dockerclient.Mount, envMaps []envPathMap) map[string]string {
	return containerenv.Translate(env, func(value string) (string, bool) {
		return translateDeclaredEnvPath(value, mountTable, envMaps)
	})
}

func translateDeclaredEnvPath(value string, mountTable []dockerclient.Mount, envMaps []envPathMap) (string, bool) {
	containerValue := cleanContainerPath(value)
	alreadyVisible := isContainerPath(mountTable, value) || isEnvMappedContainerPath(envMaps, value)

	translated, translatedFromHost := hostEnvPath(value, mountTable, envMaps)
	if alreadyVisible {
		// Host and process coordinate spaces may contain the same absolute spelling.
		// If they disagree on its meaning, provenance cannot be inferred from the
		// string; dropping the declared path is safer than silently redirecting it.
		if translatedFromHost && translated != containerValue {
			return "", false
		}
		return containerValue, true
	}
	return translated, translatedFromHost
}

func hostEnvPath(value string, mountTable []dockerclient.Mount, envMaps []envPathMap) (string, bool) {
	if container, err := toContainerPath(mountTable, value); err == nil {
		return container, true
	}
	return applyEnvPathMaps(envMaps, value)
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

func isEnvMappedContainerPath(maps []envPathMap, value string) bool {
	value = cleanContainerPath(value)
	for _, mapping := range maps {
		containerPrefix := cleanContainerPath(mapping.ContainerPrefix)
		if value == containerPrefix || strings.HasPrefix(value, containerPrefix+"/") {
			return true
		}
	}
	return false
}

// dockerExecEnvironment keeps the creation-time policy in its already-rendered
// container coordinates while applying the declared path schema and drop list
// to every per-call override. Unknown variables remain literals by contract.
func dockerExecEnvironment(policyEnv, overrides map[string]string, mountTable []dockerclient.Mount, envMaps []envPathMap, toolBinPaths []string) map[string]string {
	return dockerExecEnvironmentMode(policyEnv, overrides, sandboxpkg.EnvOverlay, mountTable, envMaps, toolBinPaths, toolBinPaths)
}

func dockerExecEnvironmentMode(policyEnv, overrides map[string]string, mode sandboxpkg.EnvMode, mountTable []dockerclient.Mount, envMaps []envPathMap, toolBinPaths, coreToolBinPaths []string) map[string]string {
	overrides = translateEnvPaths(overrides, mountTable, envMaps)
	if mode == sandboxpkg.EnvReplace {
		// EnvReplace must forget the previous turn's optional package paths. The
		// current turn carries its authorized public bins through the translated
		// marker variables; PATH itself is always dropped as a host-provided value.
		selectionPaths := append(selectionPathsFromEnv(overrides, sandboxpkg.EnvUserNativeSelectionDir), selectionPathsFromEnv(overrides, sandboxpkg.EnvNativeSelectionDir)...)
		return injectToolPaths(overrides, append(selectionPaths, coreToolBinPaths...))
	}
	return injectToolPaths(mergeEnv(policyEnv, overrides), toolBinPaths)
}

func selectionPathsFromEnv(env map[string]string, key string) []string {
	value := env[key]
	if value == "" {
		return nil
	}
	paths := make([]string, 0, strings.Count(value, ":")+1)
	for path := range strings.SplitSeq(value, ":") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// containerDefaultPATH is the image-baked system PATH from the Dockerfile ENV
// directive. Stella's shared bin and mise shims are deliberately absent: plugin
// commands enter through selection-local paths supplied by the runner snapshot.
// Keep in sync with the ENV PATH line in plugins/sandbox/docker/Dockerfile.
const containerDefaultPATH = containerenv.DefaultPATH

// injectToolPaths adds only the selected container-native commands.
func injectToolPaths(env map[string]string, toolBinPaths []string) map[string]string {
	return containerenv.WithToolPaths(env, toolBinPaths)
}

// envPathMap is an extra host→container path translation that translateEnvPaths
// applies before consulting the mount table. Used for STELLA_HOME which needs
// env translation but must NOT be in the mount table (that would allow file
// reads across the entire directory).
type envPathMap struct {
	HostPrefix      string
	ContainerPrefix string
}
