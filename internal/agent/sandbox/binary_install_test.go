package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/toolinstall"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func testContextSelectionPlan(stellaHome string, specs []plugins.PluginBinarySpec) (BinarySelectionPlan, error) {
	dataDir := pkgsandbox.MiseToolsDir(stellaHome)
	identity, err := binarySelectionIdentity(specs, dataDir)
	if err != nil {
		return BinarySelectionPlan{}, err
	}
	return selectionPlan(identity, BinaryPackage{}, dataDir, filepath.Join(dataDir, "public")), nil
}

func TestBinarySelectionIdentitySeparatesPrincipalAndPin(t *testing.T) {
	base := plugins.PluginBinarySpec{
		Name: "gh", Tool: "github:cli/cli", Version: "1.0.0",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "pkg@digest-a", ConfigID: "config-a", Scope: string(plugin.ScopeUser), Revision: 1,
		},
	}
	first, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base}, "/users/alice/.mise-tools")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base}, "/users/bob/.mise-tools")
	if err != nil {
		t.Fatal(err)
	}
	pinned := base
	pinned.Version = "2.0.0"
	otherPin, err := binarySelectionIdentity([]plugins.PluginBinarySpec{pinned}, "/users/alice/.mise-tools")
	if err != nil {
		t.Fatal(err)
	}
	if first == otherUser || first == otherPin {
		t.Fatalf("principal and pin must isolate CLI selections: first=%q otherUser=%q otherPin=%q", first, otherUser, otherPin)
	}
}

func TestBinarySelectionIdentityNormalizesLatest(t *testing.T) {
	base := plugins.PluginBinarySpec{
		Name: "gh", Tool: "github:cli/cli",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "pkg@digest-a", ConfigID: "config-a", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	implicit, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	base.Version = "latest"
	explicit, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	if implicit != explicit {
		t.Fatalf("implicit latest drifted selection identity: %q != %q", implicit, explicit)
	}
}

func TestLatestSelectionReusesPublishedResult(t *testing.T) {
	stellaHome := t.TempDir()
	spec := plugins.PluginBinarySpec{
		Name: "uv", Tool: "uv", PackageDigest: "sha256:package-a",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "uv", ConfigID: "config-a", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	implicit, err := testContextSelectionPlan(stellaHome, []plugins.PluginBinarySpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	spec.Version = "latest"
	explicit, err := testContextSelectionPlan(stellaHome, []plugins.PluginBinarySpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	if implicit.Identity != explicit.Identity || implicit.PublicDir != explicit.PublicDir {
		t.Fatalf("latest selection changed published location: implicit=%+v explicit=%+v", implicit, explicit)
	}
	if err := os.MkdirAll(implicit.PublicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(implicit.PublicDir, "uv"), []byte("selected"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(stellaHome, "mise-called")
	if err := os.MkdirAll(filepath.Join(stellaHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stellaHome, "bin", "mise"), []byte("#!/bin/sh\nprintf called > "+called+"\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := toolinstall.InstallSelection(context.Background(), stellaHome, toolinstall.Selection{
		DataDir: implicit.DataDir, PublicDir: implicit.PublicDir, PublicBinDir: implicit.PublicBinDir,
	}, []toolinstall.Tool{{Key: "uv", Lookup: "uv", PublicName: "uv"}}); err != nil {
		t.Fatalf("InstallSelection reused result: %v", err)
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("latest selection rebuilt despite complete publication, stat err=%v", err)
	}
}

func TestBinarySelectionIdentityIncludesPackageDigest(t *testing.T) {
	base := plugins.PluginBinarySpec{
		Name: "gh", Tool: "github:cli/cli", Version: "1.0.0", PackageDigest: "sha256:package-a",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "pkg", ConfigID: "config-a", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	first, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	base.PackageDigest = "sha256:package-b"
	second, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("package digest changed without changing selection identity: %q", first)
	}
	other := base
	other.PluginID = "other"
	got, err := binarySelectionIdentity([]plugins.PluginBinarySpec{base, other}, "")
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := binarySelectionIdentity([]plugins.PluginBinarySpec{other, base}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != reverse {
		t.Fatalf("selection identity depends on input order: %q != %q", got, reverse)
	}
}

func TestBinarySelectionIdentityRejectsInvalidPin(t *testing.T) {
	spec := plugins.PluginBinarySpec{
		Name: "gh", Tool: "github:cli/cli", Version: "../../latest",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "pkg@digest-a", ConfigID: "config-a", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	if _, err := binarySelectionIdentity([]plugins.PluginBinarySpec{spec}, ""); err == nil || !strings.Contains(err.Error(), "published version or tag") {
		t.Fatalf("invalid pin error = %v, want explicit pin rejection", err)
	}
}

func TestContextBinaryInstallPlanIdentityIncludesSelectionRevision(t *testing.T) {
	base := plugins.PluginBinarySpec{
		Name: "fd", Tool: "github:sharkdp/fd", Version: "1.0.0",
		PluginResourceIdentity: plugins.PluginResourceIdentity{
			PluginID: "fd", ConfigID: "fd-system", Scope: string(plugin.ScopeSystem), Revision: 1,
		},
	}
	first, err := testContextSelectionPlan(t.TempDir(), []plugins.PluginBinarySpec{base})
	if err != nil {
		t.Fatal(err)
	}
	base.Revision++
	second, err := testContextSelectionPlan(t.TempDir(), []plugins.PluginBinarySpec{base})
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
	if _, err := testContextSelectionPlan(t.TempDir(), []plugins.PluginBinarySpec{spec}); err == nil || !strings.Contains(err.Error(), "unsafe path name") {
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
	plan := BinaryInstallPlan{Selections: []BinarySelectionPlan{{PublicDir: "/selected", PublicBinDir: "/selected"}, {PublicDir: "/other", PublicBinDir: "/other"}}}
	wantSelection := strings.Join([]string{"/selected", "/other"}, string(filepath.ListSeparator))
	env := OverlayBinaryInstallPlan(base, plan, BinarySystemLayer)
	if got := env[pkgsandbox.EnvNativeSelectionDir]; got != wantSelection {
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
	if got := user[pkgsandbox.EnvUserNativeSelectionDir]; got != wantSelection {
		t.Fatalf("user selection marker = %q", got)
	}
	if got := user["MISE_DATA_DIR"]; got != "/users/u/.mise-tools" {
		t.Fatalf("user mise data dir = %q", got)
	}
}

func TestInstallPlanPathsStayUnderMiseTree(t *testing.T) {
	home := t.TempDir()
	plan, err := testContextSelectionPlan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.PublicDir, filepath.Join(home, ".mise-tools")+string(filepath.Separator)) {
		t.Fatalf("public selection escaped mise tree: %q", plan.PublicDir)
	}
}
