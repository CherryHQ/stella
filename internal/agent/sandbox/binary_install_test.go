package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestContextBinaryInstallPlanIdentityIncludesSelectionRevision(t *testing.T) {
	base := plugins.PluginBinarySpec{
		Name: "fd", Tool: "github:sharkdp/fd", Version: "1.0.0",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "fd", ConfigID: "fd-system", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	first, err := ContextBinaryInstallPlan(t.TempDir(), []plugins.PluginBinarySpec{base})
	if err != nil {
		t.Fatal(err)
	}
	base.Revision++
	second, err := ContextBinaryInstallPlan(t.TempDir(), []plugins.PluginBinarySpec{base})
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity == second.Identity {
		t.Fatal("selection identity ignored resource revision")
	}
}

func TestContextBinaryInstallPlanRejectsUnsafeNames(t *testing.T) {
	spec := plugins.PluginBinarySpec{
		Name: "../escape", Tool: "uv",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "uv", ConfigID: "uv-system", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	if _, err := ContextBinaryInstallPlan(t.TempDir(), []plugins.PluginBinarySpec{spec}); err == nil || !strings.Contains(err.Error(), "unsafe path name") {
		t.Fatalf("unsafe binary name error = %v", err)
	}
}

func TestOverlayBinaryInstallPlanKeepsSelectionPathsAndClearsPrivateState(t *testing.T) {
	base := map[string]string{
		"PATH":                      "/usr/bin",
		"MISE_DATA_DIR":             "/users/u/.mise-tools",
		"MISE_SHIMS_DIR":            "/private/shims",
		"MISE_SYSTEM_CONFIG_FILE":   "/private/system.toml",
		"MISE_TRUSTED_CONFIG_PATHS": "/private/system.toml:/workspace",
	}
	plan := BinaryInstallPlan{PublicDir: "/selected", PublicBinDir: "/selected"}
	env := OverlayBinaryInstallPlan(base, plan, BinarySystemLayer)
	if got := env[pkgsandbox.EnvNativeSelectionDir]; got != "/selected" {
		t.Fatalf("native selection marker = %q", got)
	}
	if _, ok := env["MISE_SHIMS_DIR"]; ok {
		t.Fatal("private shims leaked")
	}
	if _, ok := env["MISE_SYSTEM_CONFIG_FILE"]; ok {
		t.Fatal("private system config leaked")
	}
	if got := env[pkgsandbox.EnvRunnerPath]; got != env["PATH"] {
		t.Fatalf("runner path = %q, PATH = %q", got, env["PATH"])
	}

	user := OverlayBinaryInstallPlan(base, plan, BinaryUserLayer)
	if got := user[pkgsandbox.EnvUserNativeSelectionDir]; got != "/selected" {
		t.Fatalf("user selection marker = %q", got)
	}
	if got := user["MISE_DATA_DIR"]; got != "/users/u/.mise-tools" {
		t.Fatalf("user mise data dir = %q", got)
	}
}

func TestInstallPlanPathsStayUnderMiseTree(t *testing.T) {
	home := t.TempDir()
	plan, err := ContextBinaryInstallPlan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.PublicDir, filepath.Join(home, ".mise-tools")+string(filepath.Separator)) {
		t.Fatalf("public selection escaped mise tree: %q", plan.PublicDir)
	}
}
