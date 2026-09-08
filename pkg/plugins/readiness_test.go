package plugins

import "testing"

func TestPreparationMergePublishesFrozenBinaryEvidenceAndDropsFailedPackage(t *testing.T) {
	base := PluginPreparationResult{Packages: []PluginPackageStatus{{PluginID: "pkg", Ready: true}}}
	merged := base.Merge(PluginPreparationResult{
		Packages: []PluginPackageStatus{{PluginID: "pkg", Reason: "CLI preparation failed"}},
		Binaries: []PluginBinaryPreparation{{
			PluginResourceIdentity: PluginResourceIdentity{PluginID: "pkg", ConfigID: "cfg", Scope: "user", Revision: 2},
			Name:                   "uv", Tool: "uv", RequestedVersion: "latest", ResolvedVersion: "0.6.14",
		}},
	})
	if len(merged.Binaries) != 0 {
		t.Fatalf("failed package retained binary evidence: %+v", merged.Binaries)
	}
	merged = PluginPreparationResult{Packages: []PluginPackageStatus{{PluginID: "pkg", Ready: true}}}.Merge(PluginPreparationResult{
		Binaries: []PluginBinaryPreparation{{PluginResourceIdentity: PluginResourceIdentity{PluginID: "pkg"}, Name: "uv", Tool: "uv", ResolvedVersion: "0.6.14"}},
	})
	if len(merged.Binaries) != 1 || merged.Binaries[0].ResolvedVersion != "0.6.14" {
		t.Fatalf("binary evidence = %+v", merged.Binaries)
	}
}
