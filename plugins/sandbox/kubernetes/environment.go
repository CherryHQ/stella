package kubernetes

import (
	"path"
	"path/filepath"
	"strings"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Only runner-owned path values are translated. Vault values remain literals,
// even when their spelling happens to resemble a host path.
func (s *session) environment(overrides map[string]string) map[string]string {
	env := make(map[string]string, len(s.policy.Env)+len(overrides))
	for _, source := range []map[string]string{s.policy.Env, overrides} {
		for key, value := range source {
			switch key {
			case "PATH", "STELLA_RUNNER_PATH", "STELLA_USER_DIR":
				continue
			case "HOME", "STELLA_HOME", "STELLA_ASSETS_DIR", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "BASH_ENV", "MISE_DATA_DIR", "MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_SYSTEM_CONFIG_FILE", "MISE_GLOBAL_CONFIG_FILE":
				if translated, ok := s.envPath(value); ok {
					env[key] = translated
				} else {
					delete(env, key)
				}
			case "MISE_TRUSTED_CONFIG_PATHS":
				var values []string
				for v := range strings.SplitSeq(value, ":") {
					if translated, ok := s.envPath(v); ok {
						values = append(values, translated)
					}
				}
				env[key] = strings.Join(values, ":")
			default:
				env[key] = value
			}
		}
	}
	if env["HOME"] == "" {
		env["HOME"] = "/workspace"
	}
	if env["TMPDIR"] == "" {
		env["TMPDIR"] = "/tmp"
	}
	env["STELLA_HOME"] = "/opt/stella"
	var bins []string
	for _, m := range s.policy.Filesystem.Mounts {
		if m.Access == sandbox.MountReadWrite && path.Base(m.SandboxPath) == ".mise-tools" {
			bins = append(bins, path.Join(m.SandboxPath, "shims"))
		}
	}
	bins = append(bins, "/opt/stella/bin:/opt/stella/.mise-tools/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	env["PATH"] = strings.Join(bins, ":")
	env[sandbox.EnvRunnerPath] = env["PATH"]
	if s.policy.NetworkModeOrDefault() == sandbox.NetworkDisabled {
		delete(env, "STELLA_SERVER_URL")
	}
	return env
}

func (s *session) envPath(value string) (string, bool) {
	if value == "" || !filepath.IsAbs(value) {
		return "", false
	}
	for visible, source := range s.sources {
		if rel, err := filepath.Rel(source, value); err == nil && filepath.IsLocal(rel) {
			return path.Join(visible, filepath.ToSlash(rel)), true
		}
	}
	if rel, err := filepath.Rel(s.client.cfg.StellaHome, value); err == nil && filepath.IsLocal(rel) {
		return path.Join("/opt/stella", filepath.ToSlash(rel)), true
	}
	for _, m := range s.policy.Filesystem.Mounts {
		if _, ok := sandbox.POSIXPathRelative(m.SandboxPath, value); ok {
			return value, true
		}
	}
	if _, ok := sandbox.POSIXPathRelative("/opt/stella", value); ok {
		return value, true
	}
	return "", false
}
