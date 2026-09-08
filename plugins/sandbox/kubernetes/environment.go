package kubernetes

import (
	"maps"
	"path"
	"path/filepath"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/containerenv"
)

// Only runner-owned path values are translated. Vault values remain literals,
// even when their spelling happens to resemble a host path.
func (s *session) environment(overrides map[string]string) map[string]string {
	env := make(map[string]string, len(s.policy.Env)+len(overrides))
	maps.Copy(env, s.policy.Env)
	maps.Copy(env, overrides)
	env = containerenv.Translate(env, s.envPath)
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
	containerenv.WithToolPaths(env, bins)
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
