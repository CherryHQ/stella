package kubernetes

import (
	"context"
	"errors"
	"maps"
	"path"
	"path/filepath"
	"strings"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/containerenv"
)

// Only runner-owned path values are translated. Vault values remain literals,
// even when their spelling happens to resemble a host path.
func (s *session) environment(overrides map[string]string, mode sandbox.EnvMode) map[string]string {
	capacity := len(overrides)
	if mode == sandbox.EnvOverlay {
		capacity += len(s.policy.Env)
	}
	env := make(map[string]string, capacity)
	if mode == sandbox.EnvOverlay {
		maps.Copy(env, s.policy.Env)
	}
	maps.Copy(env, overrides)
	sharedData := ""
	for _, m := range s.policy.Filesystem.Mounts {
		if m.SandboxPath == sandbox.MountUserData {
			sharedData = sandbox.MountUserData
			break
		}
	}
	if mode == sandbox.EnvReplace {
		// Reapply the fixed filesystem contract on every turn. The logical turn
		// environment is rebuilt by the runner, so this also clears optional roots
		// that disappeared since the previous turn.
		_ = sandbox.ApplyFilesystemEnv(env, sandbox.FilesystemView{Home: sandbox.MountWorkspace, SharedDataDir: sharedData, TempDir: "/tmp"})
	}
	env = containerenv.Translate(env, s.envPath)
	if env["HOME"] == "" {
		env["HOME"] = "/workspace"
	}
	if env["TMPDIR"] == "" {
		env["TMPDIR"] = "/tmp"
	}
	env["STELLA_HOME"] = "/opt/stella"
	var bins []string
	for _, key := range []string{sandbox.EnvUserNativeSelectionDir, sandbox.EnvNativeSelectionDir, sandbox.EnvCoreRuntimeDir} {
		for value := range strings.SplitSeq(env[key], ":") {
			if value != "" {
				bins = append(bins, value)
			}
		}
	}
	for _, m := range s.policy.Filesystem.Mounts {
		if m.Access == sandbox.MountReadWrite && path.Base(m.SandboxPath) == ".mise-tools" {
			bins = append(bins, path.Join(m.SandboxPath, "shims"))
		}
	}
	containerenv.WithToolPaths(env, bins)
	if s.client != nil && s.client.cfg.ServerURL != "" && s.policy.NetworkModeOrDefault() != sandbox.NetworkDisabled {
		env["STELLA_SERVER_URL"] = s.client.cfg.ServerURL
	}
	if s.policy.NetworkModeOrDefault() == sandbox.NetworkDisabled {
		delete(env, "STELLA_SERVER_URL")
	}
	return env
}

// RenderEnv applies the fixed Kubernetes filesystem and network view to a
// fresh logical turn environment without consulting retained policy values.
func (s *session) RenderEnv(_ context.Context, logicalEnv map[string]string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.invalid {
		return nil, errors.New("kubernetes: execution generation is invalid")
	}
	return s.environment(logicalEnv, sandbox.EnvReplace), nil
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
