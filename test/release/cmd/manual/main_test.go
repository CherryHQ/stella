//go:build capability

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/test/capabilities"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestManifestManualScenariosSelectsOnlyManualPolicy(t *testing.T) {
	manifest := &capabilities.Manifest{
		Capabilities: []capabilities.Capability{{
			ID: "X06",
			Scenarios: []capabilities.Scenario{
				{ID: "X06-S01", ReleasePolicy: "blocking"},
				{ID: "X06-S02", ReleasePolicy: "manual"},
			},
		}},
	}
	scenarios, ids := manifestManualScenarios(manifest)
	if len(scenarios) != 1 || scenarios[0].ScenarioID != "X06-S02" || !ids["X06-S02"] {
		t.Fatalf("unexpected manual scenarios: %+v %v", scenarios, ids)
	}
}

func TestCurrentWaivableResultRejectsStickyProductFailure(t *testing.T) {
	results := []releasecontract.Result{
		automaticResult(releasecontract.StatusProductFailure, 1),
		automaticResult(releasecontract.StatusFlaky, 2),
	}
	_, err := currentWaivableResult(results, "X12-S02")
	if err == nil || !strings.Contains(err.Error(), "Product Failure") {
		t.Fatalf("expected Product Failure rejection, got %v", err)
	}
}

func TestCurrentWaivableResultUsesLatestEligibleAttempt(t *testing.T) {
	results := []releasecontract.Result{
		automaticResult(releasecontract.StatusExternalBlocked, 1),
		automaticResult(releasecontract.StatusFlaky, 2),
	}
	result, err := currentWaivableResult(results, "X12-S02")
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempt != 2 || result.Status != releasecontract.StatusFlaky {
		t.Fatalf("unexpected current result: %+v", result)
	}
}

func automaticResult(status releasecontract.Status, attempt int) releasecontract.Result {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	return releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run: releasecontract.Run{
			ID:      "release-1",
			Version: "v1.2.3",
			Commit:  "0123456789abcdef0123456789abcdef01234567",
		},
		Platforms:    []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID: "X12",
		ScenarioID:   "X12-S02",
		Runner:       releasecontract.Runner{Kind: releasecontract.RunnerLive, Name: "provider"},
		Attempt:      attempt,
		StartedAt:    now,
		FinishedAt:   now.Add(time.Second),
		Status:       status,
		Reason:       "controlled outcome",
	}
}
