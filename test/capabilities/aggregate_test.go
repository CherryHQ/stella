//go:build capability

package capabilities

import (
	"strings"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestBuildGateReportAppliesEveryReleaseOutcome(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		result     *releasecontract.Result
		wantStatus string
		wantAllow  bool
	}{
		{
			name:       "pass allows blocking scenario",
			policy:     "blocking",
			result:     gateResult(releasecontract.StatusPass, 1),
			wantStatus: "pass",
			wantAllow:  true,
		},
		{
			name:       "product failure blocks nonblocking scenario",
			policy:     "nonblocking",
			result:     gateResult(releasecontract.StatusProductFailure, 1),
			wantStatus: "product_failure",
			wantAllow:  false,
		},
		{
			name:       "external block is recorded by nonblocking policy",
			policy:     "nonblocking",
			result:     gateResult(releasecontract.StatusExternalBlocked, 1),
			wantStatus: "external_blocked",
			wantAllow:  true,
		},
		{
			name:       "not run blocks a blocking scenario",
			policy:     "blocking",
			result:     gateResult(releasecontract.StatusNotRun, 1),
			wantStatus: "not_run",
			wantAllow:  false,
		},
		{
			name:       "flaky is recorded by nonblocking policy",
			policy:     "nonblocking",
			result:     gateResult(releasecontract.StatusFlaky, 2),
			wantStatus: "flaky",
			wantAllow:  true,
		},
		{
			name:       "manual pending always blocks",
			policy:     "manual",
			result:     gateResult(releasecontract.StatusManualPending, 1),
			wantStatus: "manual_pending",
			wantAllow:  false,
		},
		{
			name:       "eligible waiver allows blocking scenario",
			policy:     "blocking",
			result:     waivedGateResult(),
			wantStatus: "waived",
			wantAllow:  true,
		},
		{
			name:       "missing result blocks",
			policy:     "nonblocking",
			wantStatus: missingResultStatus,
			wantAllow:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := gateManifest(test.policy)
			var results []releasecontract.Result
			if test.result != nil {
				results = append(results, *test.result)
			}
			report, err := BuildGateReport(t.TempDir(), manifest, gateTarget(), results, gateGeneratedAt())
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Scenarios) != 1 {
				t.Fatalf("scenario decisions = %d, want 1", len(report.Scenarios))
			}
			decision := report.Scenarios[0]
			if decision.Status != test.wantStatus || decision.Allowed != test.wantAllow {
				t.Fatalf("decision = %+v, want status=%s allowed=%t", decision, test.wantStatus, test.wantAllow)
			}
			if report.ReleaseAllowed != test.wantAllow {
				t.Fatalf("release_allowed = %t, want %t", report.ReleaseAllowed, test.wantAllow)
			}
		})
	}
}

func TestBuildGateReportIgnoresAnotherRun(t *testing.T) {
	current := *gateResult(releasecontract.StatusPass, 1)
	stale := *gateResult(releasecontract.StatusProductFailure, 1)
	stale.Run.ID = "older-run"

	report, err := BuildGateReport(
		t.TempDir(),
		gateManifest("blocking"),
		gateTarget(),
		[]releasecontract.Result{stale, current},
		gateGeneratedAt(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.ReleaseAllowed || report.Summary.IgnoredStaleResults != 1 || report.Summary.ResultRecords != 1 {
		t.Fatalf("unexpected stale result handling: %+v", report.Summary)
	}
}

func TestBuildGateReportRejectsRunIDReusedForAnotherCommit(t *testing.T) {
	result := *gateResult(releasecontract.StatusPass, 1)
	result.Run.Commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

	_, err := BuildGateReport(
		t.TempDir(),
		gateManifest("blocking"),
		gateTarget(),
		[]releasecontract.Result{result},
		gateGeneratedAt(),
	)
	if err == nil || !strings.Contains(err.Error(), "reuses run id") {
		t.Fatalf("expected candidate mismatch, got %v", err)
	}
}

func TestBuildGateReportKeepsProductFailureStickyAcrossAttempts(t *testing.T) {
	failure := *gateResult(releasecontract.StatusProductFailure, 1)
	retry := *gateResult(releasecontract.StatusFlaky, 2)

	report, err := BuildGateReport(
		t.TempDir(),
		gateManifest("nonblocking"),
		gateTarget(),
		[]releasecontract.Result{failure, retry},
		gateGeneratedAt(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision := report.Scenarios[0]
	if report.ReleaseAllowed || decision.Status != "product_failure" || decision.Attempt != 1 {
		t.Fatalf("product failure was not sticky: %+v", decision)
	}
}

func TestWriteGateReportScansCanaryBeforeWriting(t *testing.T) {
	secret := "release-report-canary"
	report := GateReport{
		SchemaVersion: gateReportSchemaVersion,
		GeneratedAt:   gateGeneratedAt(),
		Target:        gateTarget(),
		Summary: GateSummary{
			ByStatus: map[string]int{},
			ByPolicy: map[string]int{},
		},
		Blockers: []GateBlocker{{
			ScenarioID: "C01-S01",
			Status:     "not_run",
			Reason:     "accidentally captured " + secret,
		}},
	}

	_, _, err := WriteGateReport(t.TempDir(), report, map[string]string{"CANARY_SECRET": secret})
	if err == nil || !strings.Contains(err.Error(), "CANARY_SECRET") {
		t.Fatalf("expected canary rejection, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret value leaked in error: %v", err)
	}
}

func gateManifest(policy string) *Manifest {
	status := "covered"
	if policy == "manual" {
		status = "manual-only"
	}
	return &Manifest{
		SchemaVersion: 1,
		Capabilities: []Capability{{
			ID:    "C01",
			Title: "Runtime",
			Scenarios: []Scenario{{
				ID:            "C01-S01",
				Name:          "runtime-lifecycle",
				Layer:         "system",
				Status:        status,
				ReleasePolicy: policy,
			}},
		}},
	}
}

func gateTarget() releasecontract.Run {
	return releasecontract.Run{
		ID:      "release-1",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
}

func gateResult(status releasecontract.Status, attempt int) *releasecontract.Result {
	started := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	reason := ""
	if status != releasecontract.StatusPass {
		reason = "controlled fixture outcome"
	}
	return &releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           gateTarget(),
		Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID:  "C01",
		ScenarioID:    "C01-S01",
		Runner:        releasecontract.Runner{Kind: releasecontract.RunnerSystem, Name: "fixture"},
		Attempt:       attempt,
		StartedAt:     started,
		FinishedAt:    started.Add(time.Second),
		Status:        status,
		Reason:        reason,
	}
}

func waivedGateResult() *releasecontract.Result {
	result := gateResult(releasecontract.StatusWaived, 1)
	result.Reason = "the external target was unavailable"
	result.Waiver = &releasecontract.Waiver{
		OriginalStatus: releasecontract.StatusExternalBlocked,
		Approver:       "release-owner",
		Reason:         "the candidate does not change the integration",
		Commit:         result.Run.Commit,
		ScenarioID:     result.ScenarioID,
		ApprovedAt:     result.FinishedAt.Add(time.Minute),
	}
	return result
}

func gateGeneratedAt() time.Time {
	return time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
}
