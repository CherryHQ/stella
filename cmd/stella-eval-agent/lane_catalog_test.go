package main

import (
	"fmt"
	"testing"
)

func TestSpecializedTasksShareTheFrozenLaneCatalog(t *testing.T) {
	plan := fixtureConfig{CatalogDigest: "sha256:catalog"}
	inspection := fixtureInspection{
		Version: 1, Complete: true, CatalogCount: specializedCatalogCount,
		InitializeCount: 1, ToolsListCount: 1,
	}
	providerTools := []string{"bash", "skills", "memory", "library_search", "recally"}
	for i := range specializedCatalogCount {
		providerTools = append(providerTools, fmt.Sprintf("mcp__harbor-specialized-fixture__tool_%02d", i))
	}
	wantCapability := digestProfile([]string{"share"}, "bundle")
	var first result
	for _, task := range []specializedTask{taskSkillBashGuard, taskMemoryLibraryEvidence, taskMCPRecally} {
		got := result{
			ToolStrategy:                    "native",
			ProviderSurfaceCount:            len(providerTools),
			ProviderSurfaceTools:            append([]string(nil), providerTools...),
			ProviderSurfaceDigest:           "sha256:provider-surface",
			CapabilityProfileDigest:         wantCapability,
			MCPRegistrationID:               "registration-" + string(task),
			MCPTools:                        []string{specializedFixtureRegistrationName},
			SpecializedCatalogCount:         specializedCatalogCount,
			SpecializedCatalogDigest:        plan.CatalogDigest,
			RuntimeSpecializedCatalogDigest: "sha256:runtime-specialized-catalog",
		}
		if err := assertRuntimeLaneCatalog(got); err != nil {
			t.Fatalf("%s runtime lane catalog: %v", task, err)
		}
		if err := assertMCPFixtureAdmission(got, plan, inspection); err != nil {
			t.Fatalf("%s MCP admission: %v", task, err)
		}
		if first.ToolStrategy == "" {
			first = got
			continue
		}
		if got.ProviderSurfaceDigest != first.ProviderSurfaceDigest ||
			got.RuntimeSpecializedCatalogDigest != first.RuntimeSpecializedCatalogDigest ||
			got.CapabilityProfileDigest != first.CapabilityProfileDigest {
			t.Fatalf("%s changed a lane-wide catalog digest: %+v", task, got)
		}
	}
}

func TestSpecializedLanePolicyKeepsOnlyTheFrozenBuiltinUnion(t *testing.T) {
	tools := []agentTool{
		{Name: "bash", Source: "core", Enabled: true},
		{Name: "skills", Source: "builtin", Enabled: true},
		{Name: "memory", Source: "builtin", Enabled: true},
		{Name: "library_search", Source: "builtin", Enabled: true},
		{Name: "recally", Source: "builtin", Enabled: true},
		{Name: "share", Source: "builtin", Enabled: false},
		{Name: specializedFixtureRegistrationName, Source: "mcp", Enabled: true},
	}
	if err := assertLaneToolPolicy(tools); err != nil {
		t.Fatalf("frozen lane policy rejected: %v", err)
	}
	tools[5].Enabled = true
	if err := assertLaneToolPolicy(tools); err == nil {
		t.Fatal("enabled Share was admitted to the frozen lane")
	}
	tools[5].Enabled = false
	tools = append(tools, agentTool{Name: "other-mcp", Source: "mcp", Enabled: true})
	if err := assertLaneToolPolicy(tools); err == nil {
		t.Fatal("multiple MCP registrations were admitted")
	}
}

func TestNonMCPVerifiersNeedCausalEvidenceNotMCPCalls(t *testing.T) {
	plan := fixtureConfig{CatalogDigest: "sha256:catalog"}
	admission := result{
		MCPRegistrationID:               "registration",
		MCPTools:                        []string{specializedFixtureRegistrationName},
		SpecializedCatalogCount:         specializedCatalogCount,
		SpecializedCatalogDigest:        plan.CatalogDigest,
		RuntimeSpecializedCatalogDigest: "sha256:runtime-specialized-catalog",
	}
	complete := fixtureInspection{Version: 1, Complete: true, CatalogCount: specializedCatalogCount, InitializeCount: 1, ToolsListCount: 1}
	if err := assertMCPFixtureAdmission(admission, plan, complete); err != nil {
		t.Fatalf("complete catalog admission: %v", err)
	}
	incomplete := complete
	incomplete.ToolsListCount = 0
	if err := assertMCPFixtureAdmission(admission, plan, incomplete); err == nil {
		t.Fatal("missing tools/list was admitted for a non-MCP task")
	}

	skill, _, err := newSpecializedFixture(taskSkillBashGuard, "fixture-plan-seed")
	if err != nil {
		t.Fatal(err)
	}
	if verdict, err := verifySkillBashGuard(t.Context(), bridgeArtifactBinding(t, skill.artifact), skill); err != nil || verdict.Reward != 1 {
		t.Fatalf("skill verifier = %+v, %v", verdict, err)
	}
	memory, _, err := newSpecializedFixture(taskMemoryLibraryEvidence, "fixture-plan-seed")
	if err != nil {
		t.Fatal(err)
	}
	if verdict, err := verifyMemoryLibraryEvidence(t.Context(), bridgeArtifactBinding(t, memory.artifact), memory); err != nil || verdict.Reward != 1 {
		t.Fatalf("memory verifier = %+v, %v", verdict, err)
	}
}
