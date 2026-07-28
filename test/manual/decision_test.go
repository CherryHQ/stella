package manual

import (
	"strings"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestBuildManualResultKeepsMissingDecisionPending(t *testing.T) {
	result, evidence, err := BuildManualResult(
		manualRun(),
		Scenario{CapabilityID: "X06", ScenarioID: "X06-S02"},
		Decision{},
		"",
		1,
		manualNow(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != releasecontract.StatusManualPending ||
		!strings.Contains(result.Reason, "STELLA_MANUAL_X06_S02_STATUS") ||
		evidence.Status != releasecontract.StatusManualPending {
		t.Fatalf("unexpected pending decision: %+v %+v", result, evidence)
	}
}

func TestBuildManualResultAcceptsPassAndWaiver(t *testing.T) {
	pass, _, err := BuildManualResult(
		manualRun(),
		Scenario{CapabilityID: "X06", ScenarioID: "X06-S02"},
		Decision{Status: "pass", Evidence: "https://evidence.example/run-1"},
		"release-owner",
		2,
		manualNow(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pass.Status != releasecontract.StatusPass {
		t.Fatalf("pass status = %s", pass.Status)
	}

	waived, _, err := BuildManualResult(
		manualRun(),
		Scenario{CapabilityID: "X13", ScenarioID: "X13-S02"},
		Decision{
			Status:         "waived",
			OriginalStatus: releasecontract.StatusExternalBlocked,
			Reason:         "identity tenant maintenance",
			Evidence:       "https://status.example/incident",
		},
		"release-owner",
		1,
		manualNow(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if waived.Status != releasecontract.StatusWaived ||
		waived.Waiver == nil ||
		waived.Waiver.OriginalStatus != releasecontract.StatusExternalBlocked {
		t.Fatalf("unexpected waiver: %+v", waived)
	}
}

func TestParseWaiverRequestsRejectsProductFailure(t *testing.T) {
	_, err := ParseWaiverRequests(`[
  {
    "scenario_id": "X12-S02",
    "original_status": "product_failure",
    "reason": "do not allow",
    "evidence": "run log"
  }
]`)
	if err == nil || !strings.Contains(err.Error(), "not waivable") {
		t.Fatalf("expected product failure rejection, got %v", err)
	}
}

func TestBuildWaiverResultRequiresCurrentStatus(t *testing.T) {
	original := releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           manualRun(),
		Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID:  "X12",
		ScenarioID:    "X12-S02",
		Runner:        releasecontract.Runner{Kind: releasecontract.RunnerLive, Name: "provider"},
		Attempt:       1,
		StartedAt:     manualNow().Add(-time.Minute),
		FinishedAt:    manualNow().Add(-time.Second),
		Status:        releasecontract.StatusNotRun,
		Reason:        "credentials missing",
	}
	_, _, err := BuildWaiverResult(
		original,
		WaiverRequest{
			ScenarioID:     "X12-S02",
			OriginalStatus: releasecontract.StatusExternalBlocked,
			Reason:         "provider incident",
			Evidence:       "status page",
		},
		"release-owner",
		2,
		manualNow(),
	)
	if err == nil || !strings.Contains(err.Error(), "current result is not_run") {
		t.Fatalf("expected current status mismatch, got %v", err)
	}
}

func manualRun() releasecontract.Run {
	return releasecontract.Run{
		ID:      "release-1",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
}

func manualNow() time.Time {
	return time.Date(2026, time.July, 28, 13, 0, 0, 0, time.UTC)
}
