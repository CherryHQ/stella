package release

import (
	"strings"
	"testing"
	"time"
)

func TestResultValidateAcceptsCanonicalPass(t *testing.T) {
	result := validResult()
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResultValidateRejectsInvalidOutcomeContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Result)
		want   string
	}{
		{
			name: "scenario belongs to another capability",
			mutate: func(result *Result) {
				result.ScenarioID = "C02-S01"
			},
			want: "must belong to capability C01",
		},
		{
			name: "duplicate platform",
			mutate: func(result *Result) {
				result.Platforms = append(result.Platforms, result.Platforms[0])
			},
			want: "repeats platform linux/amd64",
		},
		{
			name: "retry cannot silently pass",
			mutate: func(result *Result) {
				result.Attempt = 2
			},
			want: "a retry that succeeds must be flaky",
		},
		{
			name: "flaky requires retry",
			mutate: func(result *Result) {
				result.Status = StatusFlaky
				result.Reason = "the first attempt timed out"
			},
			want: "flaky status requires attempt 2 or later",
		},
		{
			name: "non-pass needs reason",
			mutate: func(result *Result) {
				result.Status = StatusExternalBlocked
			},
			want: "requires a reason",
		},
		{
			name: "artifact stays in current run",
			mutate: func(result *Result) {
				result.Artifacts = []Artifact{{Kind: "server-log", Path: "dist/test-results/release/other/log.txt"}}
			},
			want: "must stay below dist/test-results/release/release-1",
		},
		{
			name: "product failure is not waivable",
			mutate: func(result *Result) {
				result.Status = StatusWaived
				result.Reason = "the candidate returned the wrong response"
				result.Waiver = &Waiver{
					OriginalStatus: StatusProductFailure,
					Approver:       "release-owner",
					Reason:         "do not allow this",
					Commit:         result.Run.Commit,
					ScenarioID:     result.ScenarioID,
					ApprovedAt:     result.FinishedAt.Add(time.Minute),
				}
			},
			want: `original_status "product_failure" is not waivable`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validResult()
			test.mutate(&result)
			err := result.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestResultValidateAcceptsExplicitWaiver(t *testing.T) {
	result := validResult()
	result.Status = StatusWaived
	result.Reason = "the registered external target was unavailable"
	result.Waiver = &Waiver{
		OriginalStatus: StatusExternalBlocked,
		Approver:       "release-owner",
		Reason:         "the release does not change this integration",
		Commit:         result.Run.Commit,
		ScenarioID:     result.ScenarioID,
		ApprovedAt:     result.FinishedAt.Add(time.Minute),
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func validResult() Result {
	started := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	return Result{
		SchemaVersion: SchemaVersion,
		Run: Run{
			ID:      "release-1",
			Version: "v1.2.3",
			Commit:  "0123456789abcdef0123456789abcdef01234567",
		},
		Platforms:    []Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID: "C01",
		ScenarioID:   "C01-S01",
		Runner:       Runner{Kind: RunnerSystem, Name: "system-suite"},
		Attempt:      1,
		StartedAt:    started,
		FinishedAt:   started.Add(time.Second),
		Status:       StatusPass,
	}
}
