package docker

import (
	"maps"
	"path"
	"path/filepath"
	"strings"

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
	overrides = translateEnvPaths(overrides, mountTable, envMaps)
	return injectToolPaths(mergeEnv(policyEnv, overrides), toolBinPaths)
}

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
