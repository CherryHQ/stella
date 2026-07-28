//go:build capability

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/test/capabilities"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestBuildCorePlanSeparatesSuitesSpecializedAndBacklog(t *testing.T) {
	manifest := &capabilities.Manifest{
		Capabilities: []capabilities.Capability{{
			ID: "C01",
			Scenarios: []capabilities.Scenario{
				{ID: "C01-S01", Layer: "system", Status: "covered", ReleasePolicy: "blocking"},
				{ID: "C01-S02", Layer: "integration", Status: "covered", ReleasePolicy: "blocking"},
				{ID: "C01-S03", Layer: "browser", Status: "missing", ReleasePolicy: "nonblocking", Gap: "No UI journey."},
			},
		}, {
			ID: "C02",
			Scenarios: []capabilities.Scenario{
				{ID: "C02-S02", Layer: "browser", Status: "covered", ReleasePolicy: "blocking"},
			},
		}},
	}
	plan, err := buildCorePlan(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 2 ||
		len(plan.Groups[0].Scenarios) != 1 ||
		plan.Groups[0].Scenarios[0].Scenario.ID != "C01-S02" ||
		len(plan.Groups[1].Scenarios) != 1 ||
		plan.Groups[1].Scenarios[0].Scenario.ID != "C01-S01" {
		t.Fatalf("unexpected groups: %+v", plan.Groups)
	}
	if len(plan.Backlog) != 1 || plan.Backlog[0].Scenario.ID != "C01-S03" {
		t.Fatalf("unexpected backlog: %+v", plan.Backlog)
	}
}

func TestBuildCorePlanRejectsCoveredScenarioWithoutOwner(t *testing.T) {
	manifest := &capabilities.Manifest{
		Capabilities: []capabilities.Capability{{
			ID: "C01",
			Scenarios: []capabilities.Scenario{{
				ID: "C01-S99", Layer: "browser", Status: "covered", ReleasePolicy: "blocking",
			}},
		}},
	}
	_, err := buildCorePlan(manifest)
	if err == nil || !strings.Contains(err.Error(), "has no release runner owner") {
		t.Fatalf("expected ownership error, got %v", err)
	}
}

func TestBacklogResultIsExplicitNotRun(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	result := backlogResult(
		releasecontract.Run{
			ID:      "release-1",
			Version: "v1.2.3",
			Commit:  "0123456789abcdef0123456789abcdef01234567",
		},
		1,
		scenarioRef{
			CapabilityID: "X02",
			Scenario: capabilities.Scenario{
				ID:     "X02-S01",
				Layer:  "contract",
				Status: "partial",
				Gap:    "Conformance suite is incomplete.",
			},
		},
		now,
	)
	if result.Status != releasecontract.StatusNotRun ||
		result.Runner.Kind != releasecontract.RunnerContract ||
		result.Reason != "Conformance suite is incomplete." {
		t.Fatalf("unexpected backlog result: %+v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryManifestHasCompleteCoreOwnership(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := capabilities.LoadManifest(filepath.Join(root, "test", "capabilities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildCorePlan(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups[0].Scenarios) == 0 || len(plan.Groups[1].Scenarios) == 0 {
		t.Fatalf("repository core plan unexpectedly empty: %+v", plan)
	}

	allScenarios := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		for _, scenario := range capability.Scenarios {
			allScenarios[scenario.ID] = true
		}
	}

	// Every manifest Scenario must have exactly one release result producer.
	// This keeps a newly added capability from silently disappearing between
	// the inventory and the final aggregate gate.
	owners := map[string]string{}
	addOwner := func(scenarioID, owner string) {
		t.Helper()
		if previous := owners[scenarioID]; previous != "" {
			t.Errorf("Scenario %s has duplicate release owners %s and %s", scenarioID, previous, owner)
			return
		}
		owners[scenarioID] = owner
	}
	for _, group := range plan.Groups {
		for _, scenario := range group.Scenarios {
			addOwner(scenario.Scenario.ID, "core:"+group.Name)
		}
	}
	for _, scenario := range plan.Backlog {
		addOwner(scenario.Scenario.ID, "core:backlog")
	}
	for scenarioID := range specializedScenarios {
		if !allScenarios[scenarioID] {
			t.Errorf("specialized Scenario %s is missing from repository manifest", scenarioID)
			continue
		}
		addOwner(scenarioID, "specialized:"+specializedScenarios[scenarioID])
	}
	for _, capability := range manifest.Capabilities {
		for _, scenario := range capability.Scenarios {
			switch {
			case scenario.ReleasePolicy == "manual":
				addOwner(scenario.ID, "manual")
			case scenario.Layer == "live":
				addOwner(scenario.ID, "live")
			}
		}
	}
	for scenarioID := range allScenarios {
		if owners[scenarioID] == "" {
			t.Errorf("Scenario %s has no release result owner", scenarioID)
		}
	}
	if len(owners) != len(allScenarios) {
		t.Fatalf("release ownership covers %d Scenarios; manifest contains %d", len(owners), len(allScenarios))
	}
}
