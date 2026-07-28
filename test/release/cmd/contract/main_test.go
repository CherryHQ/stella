//go:build linux

package main

import "testing"

func TestValidateContractGroupsRejectsDuplicateScenario(t *testing.T) {
	groups := []contractGroup{
		{
			Name:      "one",
			Scenarios: []scenarioRef{{CapabilityID: "X01", ScenarioID: "X01-S01"}},
			Commands:  []contractCommand{{Args: []string{"go", "test"}}},
		},
		{
			Name:      "two",
			Scenarios: []scenarioRef{{CapabilityID: "X01", ScenarioID: "X01-S01"}},
			Commands:  []contractCommand{{Args: []string{"go", "test"}}},
		},
	}
	if err := validateContractGroups(groups); err == nil {
		t.Fatal("duplicate Scenario was accepted")
	}
}

func TestContractGroupsMapDeterministicScenariosOnce(t *testing.T) {
	if err := validateContractGroups(contractGroups); err != nil {
		t.Fatalf("validate contract groups: %v", err)
	}
	got := map[string]bool{}
	for _, group := range contractGroups {
		for _, scenario := range group.Scenarios {
			got[scenario.ScenarioID] = true
		}
	}
	for _, want := range []string{
		"X07-S01",
		"X09-S01",
		"X09-S02",
		"X15-S01",
		"X15-S02",
		"X19-S02",
	} {
		if !got[want] {
			t.Errorf("contract runner does not map %s", want)
		}
	}
}
