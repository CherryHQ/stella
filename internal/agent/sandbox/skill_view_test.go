package sandbox

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
)

func TestResolveSkillViewUsesExactBuiltinBundle(t *testing.T) {
	paths := Paths{StellaHome: "/srv/stella", BuiltinBundle: "/srv/stella/bundles/revision", UserDataDir: "/user"}
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
	paths := Paths{StellaHome: "/srv/stella", BuiltinBundle: "/srv/stella/bundles/revision", WorkDir: "/work"}
	layout := runnerHostLayout(paths, Config{})
	for _, mount := range layout.Mounts {
		if mount.Target == pkgsandbox.MountBuiltinSkills {
			if mount.Source != paths.BuiltinBundle || mount.Access != hostlayout.ReadOnly {
				t.Fatalf("builtin mount = %#v", mount)
			}
			return
		}
		if mount.Source == filepath.Join(paths.StellaHome, ".agents", "skills") {
			t.Fatalf("legacy builtin host path leaked into policy: %#v", mount)
		}
	}
	t.Fatal("missing builtin bundle mount")
}
