package live

import (
	"context"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestExecutorRecordsMissingResourcesWithoutCallingAdapter(t *testing.T) {
	adapter := &fakeAdapter{}
	executions, err := (Executor{
		Adapters:        map[string]Adapter{"fake": adapter},
		LookupEnv:       func(string) (string, bool) { return "", false },
		Now:             fixedClock(),
		WorkflowAttempt: 1,
	}).Execute(context.Background(), liveRun(), liveRegistry("fake"))
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 || executions[0].Result.Status != releasecontract.StatusNotRun {
		t.Fatalf("unexpected executions: %+v", executions)
	}
	if adapter.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.calls)
	}
}

func TestExecutorRecordsPendingAdapter(t *testing.T) {
	registry := liveRegistry(PendingAdapter)
	executions, err := (Executor{
		Adapters:        map[string]Adapter{},
		LookupEnv:       configuredLookup,
		Now:             fixedClock(),
		WorkflowAttempt: 1,
	}).Execute(context.Background(), liveRun(), registry)
	if err != nil {
		t.Fatal(err)
	}
	if got := executions[0].Result; got.Status != releasecontract.StatusNotRun ||
		got.Reason != "live adapter is pending implementation for target provider-smoke" {
		t.Fatalf("unexpected pending result: %+v", got)
	}
}

func TestExecutorRetriesExternalBlockAndMarksSuccessFlaky(t *testing.T) {
	adapter := &fakeAdapter{
		runErrors: []error{ExternalBlocked("provider timed out", true), nil},
	}
	executions, err := (Executor{
		Adapters:        map[string]Adapter{"fake": adapter},
		LookupEnv:       configuredLookup,
		Now:             fixedClock(),
		WorkflowAttempt: 1,
	}).Execute(context.Background(), liveRun(), liveRegistry("fake"))
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 2 {
		t.Fatalf("executions = %d, want 2", len(executions))
	}
	if executions[0].Result.Status != releasecontract.StatusExternalBlocked ||
		executions[1].Result.Status != releasecontract.StatusFlaky ||
		executions[1].Result.Attempt != 2 {
		t.Fatalf("unexpected retry results: %+v", executions)
	}
	if adapter.cleanupCalls != 2 {
		t.Fatalf("cleanup calls = %d, want 2", adapter.cleanupCalls)
	}
	for _, execution := range executions {
		if err := execution.Result.Validate(); err != nil {
			t.Fatalf("invalid result: %v", err)
		}
	}
}

func TestExecutorRedactsSecretBearingAdapterFailure(t *testing.T) {
	adapter := &fakeAdapter{
		runErrors: []error{ExternalBlocked("bad credential live-secret-value", true)},
	}
	executions, err := (Executor{
		Adapters: map[string]Adapter{"fake": adapter},
		LookupEnv: func(name string) (string, bool) {
			if name == "STELLA_LIVE_PROVIDER_TARGETS_JSON" {
				return "live-secret-value", true
			}
			return "", false
		},
		Now:             fixedClock(),
		WorkflowAttempt: 1,
	}).Execute(context.Background(), liveRun(), liveRegistry("fake"))
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 {
		t.Fatalf("secret-bearing failure retried %d times; want one sticky Product Failure", len(executions))
	}
	result := executions[0].Result
	if result.Status != releasecontract.StatusProductFailure ||
		executions[0].Retryable ||
		result.Reason != "live adapter returned a secret-bearing diagnostic; unsafe detail was removed" {
		t.Fatalf("unexpected redacted result: %+v", result)
	}
}

type fakeAdapter struct {
	calls        int
	cleanupCalls int
	runErrors    []error
}

func (a *fakeAdapter) Preflight(context.Context, Target, Inputs) error {
	a.calls++
	return nil
}

func (a *fakeAdapter) Run(context.Context, Target, Inputs) error {
	a.calls++
	if len(a.runErrors) == 0 {
		return nil
	}
	err := a.runErrors[0]
	a.runErrors = a.runErrors[1:]
	return err
}

func (a *fakeAdapter) Assert(context.Context, Target, Inputs) error {
	a.calls++
	return nil
}

func (a *fakeAdapter) Cleanup(context.Context, Target, Inputs) error {
	a.calls++
	a.cleanupCalls++
	return nil
}

func liveRegistry(adapter string) *Registry {
	return &Registry{
		SchemaVersion: RegistrySchemaVersion,
		Targets: []Target{{
			ID:           "provider-smoke",
			CapabilityID: "X12",
			ScenarioID:   "X12-S02",
			Adapter:      adapter,
			Summary:      "Provider smoke target.",
			Resources: []Resource{{
				Name:        "STELLA_LIVE_PROVIDER_TARGETS_JSON",
				Secret:      true,
				Required:    true,
				Description: "Provider target fixture.",
			}},
		}},
	}
}

func liveRun() releasecontract.Run {
	return releasecontract.Run{
		ID:      "release-1",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
}

func configuredLookup(name string) (string, bool) {
	if name == "STELLA_LIVE_PROVIDER_TARGETS_JSON" {
		return `{"provider":"fixture"}`, true
	}
	return "", false
}

func fixedClock() func() time.Time {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}
}
