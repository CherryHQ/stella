package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	skillpkg "github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func capturedFileResources(t *testing.T, withInvalidMCP bool) ([]plugin.FileResource, authz.Authority) {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".agents", "plugins", "demo", "skills", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, ".agents", "skills", "independent"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"` + agentpackage.PluginSchemaV1 + `","name":"demo","version":"1.2.3","extensions":{"` + agentpackage.StellaNamespace + `":{"version":"1","prompt":"package prompt","binaries":[{"name":"demo","tool":"github:owner/demo","version":"1.0.0"}],"session_env":[{"env_var":"DEMO_TOKEN","source":"oauth.access_token","required":true}],"oauth":[{"provider":"demo","scopes":["read"]}]}}}`
	if err := os.WriteFile(filepath.Join(base, ".agents", "plugins", "demo", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, ".agents", "plugins", "demo", "skills", "docs", "SKILL.md"), []byte("---\nname: docs\ndescription: package docs\n---\n# docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, ".agents", "skills", "independent", "SKILL.md"), []byte("---\nname: independent\ndescription: independent docs\n---\n# independent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withInvalidMCP {
		mcp := `{"$schema":"` + agentpackage.MCPV1Schema + `","mcpServers":{"bad":{"type":"streamable-http","url":"https://"}}}`
		if err := os.WriteFile(filepath.Join(base, ".agents", "plugins", "demo", "mcp.json"), []byte(mcp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db := dbtest.New(t)
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources, err := plugin.DiscoverResources(context.Background(), []plugin.ResourceRoot{{
		Scope: plugin.ScopeSystem,
		Open: func(ctx context.Context) (home.RootOperations, error) {
			return manager.OpenRoot(ctx, home.WorkspaceRequest{}, home.RootSystemResources, home.RootReadOnly)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewSystemAuthority("runtime-file-test")
	if err != nil {
		t.Fatal(err)
	}
	return resources, authority
}

func fileResourceNamed(resources []plugin.FileResource, kind plugin.ResourceKind, name string) plugin.FileResource {
	for _, resource := range resources {
		if resource.Key.Kind == kind && resource.Key.Name == name {
			return resource
		}
	}
	return plugin.FileResource{}
}

func TestNewFilePluginContextProjectsCapturedPackage(t *testing.T) {
	resources, authority := capturedFileResources(t, false)
	ctx, err := NewFilePluginContext(authority, resources)
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.IsFileBased() || ctx.Authority().Component() != authority.Component() {
		t.Fatalf("file context identity = file:%v authority:%q", ctx.IsFileBased(), ctx.Authority().Component())
	}
	view := ctx.SessionPluginView()
	packageResource := fileResourceNamed(resources, plugin.ResourcePlugin, "demo")
	independentResource := fileResourceNamed(resources, plugin.ResourceSkill, "independent")
	packageID := packageResource.Key.ID()
	if !slices.Contains(view.RegisteredPluginIDs, packageID) || !slices.Contains(view.ExposedPluginIDs, packageID) {
		t.Fatalf("package visibility = %+v", view)
	}
	if len(view.BinarySpecs) != 1 || view.BinarySpecs[0].PluginID != packageID || view.BinarySpecs[0].PackageDigest != packageResource.Digest {
		t.Fatalf("binary projection = %+v", view.BinarySpecs)
	}
	if len(view.SessionEnvSpecs) != 1 || view.SessionEnvSpecs[0].EnvVar != "DEMO_TOKEN" || view.SessionEnvSpecs[0].PluginID != packageID {
		t.Fatalf("env projection = %+v", view.SessionEnvSpecs)
	}
	if len(view.PromptSections) != 1 || view.PromptSections[0].Content != "package prompt" {
		t.Fatalf("prompt projection = %+v", view.PromptSections)
	}
	if len(view.SkillSpecs) != 1 || view.SkillSpecs[0].Name != "docs" || view.SkillSpecs[0].PluginID != packageID {
		t.Fatalf("package Skill projection = %+v", view.SkillSpecs)
	}
	if got := view.PackageRequirements[0].UnavailableReason; got != "" {
		t.Fatalf("healthy package unavailable reason = %q", got)
	}
	if !slices.Contains(view.RegisteredPluginIDs, independentResource.Key.ID()) {
		t.Fatalf("independent Skill was not registered: %+v", view.RegisteredPluginIDs)
	}
	if slices.ContainsFunc(view.SkillSpecs, func(spec pkgplugins.PluginSkillSpec) bool { return spec.PluginID == independentResource.Key.ID() }) {
		t.Fatalf("independent Skill leaked into package refs: %+v", view.SkillSpecs)
	}
	copy := ctx.FileResources()
	packageCopy := 0
	for i := range copy {
		if copy[i].Key == packageResource.Key {
			packageCopy = i
			break
		}
	}
	copy[packageCopy].Package.Manifest.Name = "mutated"
	copy[packageCopy].Package.Extension.Binaries[0].Name = "mutated"
	copy[packageCopy].Diagnostics = append(copy[packageCopy].Diagnostics, agentpackage.Diagnostic{Code: "mutated"})
	copy[packageCopy].Content = nil
	got := ctx.FileResources()
	var packageGot plugin.FileResource
	for _, resource := range got {
		if resource.Key == packageResource.Key {
			packageGot = resource
			break
		}
	}
	if packageGot.Package.Manifest.Name != "demo" || packageGot.Package.Extension.Binaries[0].Name != "demo" || len(packageGot.Diagnostics) != 0 || packageGot.Content == nil {
		t.Fatalf("FileResources was not defensive: %+v", packageGot)
	}
	derived := []PluginContext{
		ctx.WithOAuthPreparationResult(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: packageID, Ready: true}}}),
		ctx.WithPreparationResult(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: packageID, Ready: true}}}),
		ctx.WithMCPToolSnapshot(pkgplugins.MCPToolSnapshot{Directory: []pkgplugins.MCPDirectoryEntry{{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: packageID}, ServerKey: "main"}}}),
	}
	for i, candidate := range derived {
		copied := candidate.FileResources()
		copiedPackage := 0
		for j := range copied {
			if copied[j].Key == packageResource.Key {
				copiedPackage = j
				break
			}
		}
		copied[copiedPackage].Package.Manifest.Version = "mutated"
		if got := candidate.FileResources(); got[copiedPackage].Package.Manifest.Version != "1.2.3" || got[copiedPackage].Digest != packageResource.Digest {
			t.Fatalf("derived context %d lost immutable capture: %+v", i, got[copiedPackage])
		}
	}
}

func TestFilePluginContextKeepsPackageAfterInvalidMCPComponent(t *testing.T) {
	resources, authority := capturedFileResources(t, true)
	ctx, err := NewFilePluginContext(authority, resources)
	if err != nil {
		t.Fatal(err)
	}
	view := ctx.SessionPluginView()
	packageResource := fileResourceNamed(resources, plugin.ResourcePlugin, "demo")
	if status := view.PackageResults.Status(packageResource.Key.ID()); !status.Ready {
		t.Fatalf("invalid MCP component masked package: %+v", status)
	}
	if len(view.SkillSpecs) != 1 || view.SkillSpecs[0].Name != "docs" {
		t.Fatalf("package Skill was masked by MCP component: %+v", view.SkillSpecs)
	}
}

func TestFilePluginContextRejectsForeignOwnerAndSummarizesPreparation(t *testing.T) {
	resources, authority := capturedFileResources(t, false)
	foreignKey := resources[0].Key
	foreignKey.Scope = plugin.ScopeUser
	foreignKey.UserID = "different-user"
	foreignKey.AgentID = ""
	for i := range resources {
		if resources[i].Key.Kind == plugin.ResourcePlugin {
			resources[i].Key = foreignKey
			break
		}
	}
	if _, err := NewFilePluginContext(authority, resources); err == nil {
		t.Fatal("foreign file resource was accepted")
	}
	resources, authority = capturedFileResources(t, false)
	ctx, err := NewFilePluginContext(authority, resources)
	if err != nil {
		t.Fatal(err)
	}
	packageResource := fileResourceNamed(resources, plugin.ResourcePlugin, "demo")
	failed := ctx.WithPreparationResult(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: packageResource.Key.ID(), Reason: "CLI unavailable"}}})
	summary := failed.ExecutionSummaryForTurn(nil)
	if len(summary.Plugins) != 1 || summary.Plugins[0].Source != "file" || summary.Plugins[0].PackageDigest != packageResource.Digest || summary.Plugins[0].Readiness != "unavailable" || summary.Plugins[0].ConfigID != "" || summary.Plugins[0].ConfigRevision != 0 {
		t.Fatalf("file execution summary = %+v", summary)
	}
}

func TestFilePluginContextSummaryUsesCapturedSkillsAndPreparationEvidence(t *testing.T) {
	resources, authority := capturedFileResources(t, false)
	ctx, err := NewFilePluginContext(authority, resources)
	if err != nil {
		t.Fatal(err)
	}
	packageResource := fileResourceNamed(resources, plugin.ResourcePlugin, "demo")
	packageID := packageResource.Key.ID()
	specs := ctx.SelectedPluginSkillSpecs()
	refs := make([]skillpkg.PackageSkillRef, 0, len(specs))
	for _, spec := range specs {
		ref, err := skillpkg.CapturePackageSkillRef(packageResource, skillpkg.PackageSkillRef{
			PackageID: spec.PluginID, PackageDigest: spec.PackageDigest, Name: spec.Name,
			Path: spec.Path, Description: spec.Description, Builtin: spec.Builtin,
		})
		if err != nil {
			t.Fatalf("capture package Skill: %v", err)
		}
		refs = append(refs, ref)
	}
	turn, err := skillpkg.CaptureSkillTurnViewFromResources(t.Context(), resources, allowFileSummaryReads{}, nil, refs, skillpkg.ViewContext{})
	if err != nil {
		t.Fatalf("capture Skill turn: %v", err)
	}
	binarySpec := ctx.SelectedPluginBinarySpecs()[0]
	ready := pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: packageID, Ready: true}}}
	prepared := ready
	prepared.Binaries = []pkgplugins.PluginBinaryPreparation{{
		PluginResourceIdentity: binarySpec.PluginResourceIdentity,
		PackageDigest:          binarySpec.PackageDigest,
		Name:                   binarySpec.Name, Tool: binarySpec.Tool,
		RequestedVersion: binarySpec.Version, ResolvedVersion: "1.2.3",
		Backend: "sandbox", SelectionIdentity: "selection-1",
	}}
	summary := ctx.WithOAuthPreparationResult(ready).WithPreparationResult(prepared).ExecutionSummaryForTurn(&turn)
	if summary == nil || len(summary.Plugins) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	item := summary.Plugins[0]
	if item.PluginID != packageID || item.ConfigScope != string(packageResource.Key.Scope) || item.PackageDigest != packageResource.Digest || item.PackageVersion != "1.2.3" || item.Authorization != "ready" || item.Readiness != "ready" {
		t.Fatalf("file plugin receipt = %+v", item)
	}
	if len(item.Binaries) != 1 || item.Binaries[0].ResolvedVersion != "1.2.3" || item.Binaries[0].Backend != "sandbox" || item.Binaries[0].SelectionIdentity != "selection-1" {
		t.Fatalf("binary receipt = %+v", item.Binaries)
	}
	assertFileSummarySkill(t, summary, "independent", "file", string(fileResourceNamed(resources, plugin.ResourceSkill, "independent").Key.Scope), "selected")
	assertFileSummarySkillVersion(t, summary, packageID, "docs", "file", "1.2.3", "selected")

	cliFailed := ctx.WithOAuthPreparationResult(ready).WithPreparationResult(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: packageID, Reason: "CLI unavailable"}}}).ExecutionSummaryForTurn(&turn)
	failedItem := cliFailed.Plugins[0]
	if failedItem.Authorization != "ready" || failedItem.Readiness != "unavailable" || len(failedItem.Binaries) != 1 || failedItem.Binaries[0].ResolvedVersion != "" || !slices.Contains(failedItem.Failures, "CLI unavailable") {
		t.Fatalf("CLI failure receipt = %+v", failedItem)
	}
	oauthFailed := ctx.WithOAuthPreparationResult(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: packageID, Reason: "OAuth unavailable"}}}).ExecutionSummaryForTurn(&turn)
	oauthItem := oauthFailed.Plugins[0]
	if oauthItem.Authorization != "unavailable" || oauthItem.Readiness != "unavailable" || !slices.Contains(oauthItem.Failures, "OAuth unavailable") {
		t.Fatalf("OAuth failure receipt = %+v", oauthItem)
	}
}

func assertFileSummarySkill(t *testing.T, summary *ai.ExecutionSummary, name, source, scope, state string) {
	t.Helper()
	for _, item := range summary.Skills {
		if item.Name == name && item.Source == source && item.Scope == scope && item.State == state {
			return
		}
	}
	t.Fatalf("Skill %s/%s/%s state %s missing from %+v", source, scope, name, state, summary.Skills)
}

func assertFileSummarySkillVersion(t *testing.T, summary *ai.ExecutionSummary, pluginID, name, source, version, state string) {
	t.Helper()
	for _, item := range summary.Skills {
		if item.PluginID == pluginID && item.Name == name && item.Source == source && item.Version == version && item.State == state {
			return
		}
	}
	t.Fatalf("package Skill %s/%s version %s state %s missing from %+v", pluginID, name, version, state, summary.Skills)
}

type allowFileSummaryReads struct{}

func (allowFileSummaryReads) BeginRead(context.Context) (skillpkg.SkillReadDecision, error) {
	return allowFileSummaryReadDecision{}, nil
}

type allowFileSummaryReadDecision struct{}

func (allowFileSummaryReadDecision) AllowRead(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}
