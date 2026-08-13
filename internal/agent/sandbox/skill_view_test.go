package sandbox

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestResolveSkillViewUsesExactBuiltinBundle(t *testing.T) {
	paths := Paths{StellaHome: "/srv/stella", BuiltinBundle: "/srv/stella/bundles/revision", WorkspaceRoot: "/work", UserDataDir: "/user"}
	for _, tt := range []struct {
		name    string
		backend string
		want    string
	}{
		{"none", config.SandboxBackendNone, paths.BuiltinBundle},
		{"docker", config.SandboxBackendDocker, pkgsandbox.MountBuiltinSkills},
	} {
		t.Run(tt.name, func(t *testing.T) {
			view := ResolveSkillView(context.Background(), Config{SandboxBackendFn: func(context.Context) string { return tt.backend }}, paths)
			if view.BuiltinSkillsHost != paths.BuiltinBundle || view.BuiltinSkillsView != tt.want {
				t.Fatalf("builtin view = host %q, view %q; want host %q, view %q", view.BuiltinSkillsHost, view.BuiltinSkillsView, paths.BuiltinBundle, tt.want)
			}
		})
	}
}

func TestRunnerFilesystemPolicyMountsExactBuiltinBundleReadOnly(t *testing.T) {
	paths := Paths{StellaHome: "/srv/stella", AgentRoot: "/srv/stella/agents/a1", BuiltinBundle: "/srv/stella/bundles/revision", WorkspaceRoot: "/work", WorkDir: "/work"}
	policy, sources := runnerFilesystemPolicy(paths, Config{})
	foundBuiltin := false
	for _, mount := range policy.Mounts {
		if mount.SandboxPath == pkgsandbox.MountBuiltinSkills {
			if sources[mount.SandboxPath] != paths.BuiltinBundle || mount.Access != pkgsandbox.MountReadOnly {
				t.Fatalf("builtin mount = %#v", mount)
			}
			foundBuiltin = true
		}
	}
	if !foundBuiltin {
		t.Fatal("missing builtin bundle mount")
	}
	for sandboxPath, source := range sources {
		for _, authority := range []string{
			filepath.Join(paths.StellaHome, ".agents", "skills"),
			filepath.Join(paths.StellaHome, ".agents", "db-skills"),
			filepath.Join(paths.AgentRoot, ".agents", "skills"),
		} {
			if source == authority {
				t.Fatalf("managed Skill authority %q leaked into private mount source %q", authority, sandboxPath)
			}
		}
	}
}
