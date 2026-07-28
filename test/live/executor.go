package live

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

// Adapter owns the real target lifecycle. Implementations must avoid logging
// raw Inputs and return classified, single-line failures suitable for a release
// report.
type Adapter interface {
	Preflight(context.Context, Target, Inputs) error
	Run(context.Context, Target, Inputs) error
	Assert(context.Context, Target, Inputs) error
	Cleanup(context.Context, Target, Inputs) error
}

// Inputs exposes only the resource values declared by one Target.
type Inputs struct {
	values          map[string]string
	run             releasecontract.Run
	candidateBinary string
}

// Value returns one declared resource without exposing the complete map.
func (i Inputs) Value(name string) (string, bool) {
	value, ok := i.values[name]
	return value, ok
}

// Run returns the immutable release identity for Run-ID-correlated messages.
func (i Inputs) Run() releasecontract.Run {
	return i.run
}

// CandidateBinary returns the exact extracted Linux amd64 candidate path.
func (i Inputs) CandidateBinary() string {
	return i.candidateBinary
}

// Failure classifies an adapter failure without relying on string matching.
// Only external blocks may be retried; Product Failure remains sticky.
type Failure struct {
	Status    releasecontract.Status
	Reason    string
	Retryable bool
}

// Error implements error.
func (f *Failure) Error() string {
	return f.Reason
}

// ExternalBlocked reports a transient or persistent third-party condition.
func ExternalBlocked(reason string, retryable bool) error {
	return &Failure{
		Status:    releasecontract.StatusExternalBlocked,
		Reason:    oneLine(reason),
		Retryable: retryable,
	}
}

// ProductFailure reports incorrect Stella or adapter behavior.
func ProductFailure(reason string) error {
	return &Failure{
		Status: releasecontract.StatusProductFailure,
		Reason: oneLine(reason),
	}
}

// Phase records one adapter lifecycle boundary without resource values.
type Phase struct {
	Name       string    `json:"name"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
}

// Execution is one append-only Scenario attempt plus safe phase diagnostics.
type Execution struct {
	TargetID     string                 `json:"target_id"`
	ResourceEnvs []string               `json:"resource_envs"`
	Phases       []Phase                `json:"phases"`
	Retryable    bool                   `json:"retryable"`
	Result       releasecontract.Result `json:"result"`
}

// Executor runs every registered target and performs at most one retry for a
// typed retryable external block.
type Executor struct {
	Adapters        map[string]Adapter
	LookupEnv       func(string) (string, bool)
	Now             func() time.Time
	WorkflowAttempt int
	CandidateBinary string
}

// Execute emits explicit Not Run records for pending adapters or missing
// resources, so the final aggregate never mistakes an absent target for Pass.
func (e Executor) Execute(ctx context.Context, run releasecontract.Run, registry *Registry) ([]Execution, error) {
	if registry == nil {
		return nil, fmt.Errorf("live registry is nil")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	if e.LookupEnv == nil {
		return nil, fmt.Errorf("live executor requires LookupEnv")
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.WorkflowAttempt < 1 {
		e.WorkflowAttempt = 1
	}

	var executions []Execution
	for _, target := range registry.Targets {
		targetExecutions, err := e.executeTarget(ctx, run, target)
		if err != nil {
			return nil, err
		}
		executions = append(executions, targetExecutions...)
	}
	return executions, nil
}

func (e Executor) executeTarget(ctx context.Context, run releasecontract.Run, target Target) ([]Execution, error) {
	inputs := Inputs{
		values:          map[string]string{},
		run:             run,
		candidateBinary: e.CandidateBinary,
	}
	secretValues := map[string]string{}
	var missing []string
	for _, resource := range target.Resources {
		value, present := e.LookupEnv(resource.Name)
		if !present || strings.TrimSpace(value) == "" {
			if resource.Required {
				missing = append(missing, resource.Name)
			}
			continue
		}
		inputs.values[resource.Name] = value
		if resource.Secret {
			secretValues[resource.Name] = value
		}
	}
	sort.Strings(missing)

	firstAttempt := (e.WorkflowAttempt-1)*2 + 1
	if len(missing) > 0 {
		execution := e.staticExecution(
			run,
			target,
			firstAttempt,
			releasecontract.StatusNotRun,
			"required target resources are not configured: "+strings.Join(missing, ", "),
		)
		return []Execution{execution}, nil
	}
	adapter, exists := e.Adapters[target.Adapter]
	if target.Adapter == PendingAdapter || !exists {
		execution := e.staticExecution(
			run,
			target,
			firstAttempt,
			releasecontract.StatusNotRun,
			"live adapter is pending implementation for target "+target.ID,
		)
		return []Execution{execution}, nil
	}

	first := e.runAttempt(ctx, run, target, adapter, inputs, secretValues, firstAttempt)
	executions := []Execution{first}
	failure := failureFromExecution(first)
	if failure == nil || !failure.Retryable {
		return executions, nil
	}

	second := e.runAttempt(ctx, run, target, adapter, inputs, secretValues, firstAttempt+1)
	if second.Result.Status == releasecontract.StatusPass {
		second.Result.Status = releasecontract.StatusFlaky
		second.Result.Reason = fmt.Sprintf(
			"passed after retrying %s: %s",
			first.Result.Status,
			first.Result.Reason,
		)
	}
	executions = append(executions, second)
	return executions, nil
}

func (e Executor) runAttempt(
	ctx context.Context,
	run releasecontract.Run,
	target Target,
	adapter Adapter,
	inputs Inputs,
	secretValues map[string]string,
	attempt int,
) Execution {
	startedAt := e.Now().UTC()
	var phases []Phase

	failure := e.runPhase(ctx, "preflight", target, inputs, adapter.Preflight, &phases)
	if failure == nil {
		failure = e.runPhase(ctx, "run", target, inputs, adapter.Run, &phases)
		if failure == nil {
			failure = e.runPhase(ctx, "assert", target, inputs, adapter.Assert, &phases)
		}
		cleanupFailure := e.runPhase(ctx, "cleanup", target, inputs, adapter.Cleanup, &phases)
		failure = mergeFailures(failure, cleanupFailure)
	}

	finishedAt := e.Now().UTC()
	status := releasecontract.StatusPass
	reason := ""
	retryable := failure != nil &&
		failure.Status == releasecontract.StatusExternalBlocked &&
		failure.Retryable
	if failure != nil {
		status = failure.Status
		reason = failure.Reason
	}
	if status == releasecontract.StatusPass && attempt > 1 {
		status = releasecontract.StatusFlaky
		reason = "release workflow retry passed after an earlier attempt"
	}
	if containsSecret(reason, secretValues) {
		status = releasecontract.StatusProductFailure
		reason = "live adapter returned a secret-bearing diagnostic; unsafe detail was removed"
		retryable = false
		for i := range phases {
			if containsSecret(phases[i].Reason, secretValues) {
				phases[i].Reason = "secret-bearing diagnostic removed"
				phases[i].Status = string(releasecontract.StatusProductFailure)
			}
		}
	}

	return Execution{
		TargetID:     target.ID,
		ResourceEnvs: target.ResourceEnvNames(),
		Phases:       phases,
		Retryable:    retryable,
		Result: releasecontract.Result{
			SchemaVersion: releasecontract.SchemaVersion,
			Run:           run,
			Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
			CapabilityID:  target.CapabilityID,
			ScenarioID:    target.ScenarioID,
			Runner:        releasecontract.Runner{Kind: releasecontract.RunnerLive, Name: target.ID},
			Attempt:       attempt,
			StartedAt:     startedAt,
			FinishedAt:    finishedAt,
			Status:        status,
			Reason:        reason,
		},
	}
}

type phaseFunc func(context.Context, Target, Inputs) error

func (e Executor) runPhase(
	ctx context.Context,
	name string,
	target Target,
	inputs Inputs,
	run phaseFunc,
	phases *[]Phase,
) *Failure {
	startedAt := e.Now().UTC()
	err := run(ctx, target, inputs)
	finishedAt := e.Now().UTC()
	phase := Phase{Name: name, StartedAt: startedAt, FinishedAt: finishedAt, Status: "pass"}
	failure := classifyFailure(err)
	if failure != nil {
		phase.Status = string(failure.Status)
		phase.Reason = failure.Reason
	}
	*phases = append(*phases, phase)
	return failure
}

func (e Executor) staticExecution(
	run releasecontract.Run,
	target Target,
	attempt int,
	status releasecontract.Status,
	reason string,
) Execution {
	now := e.Now().UTC()
	return Execution{
		TargetID:     target.ID,
		ResourceEnvs: target.ResourceEnvNames(),
		Result: releasecontract.Result{
			SchemaVersion: releasecontract.SchemaVersion,
			Run:           run,
			Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
			CapabilityID:  target.CapabilityID,
			ScenarioID:    target.ScenarioID,
			Runner:        releasecontract.Runner{Kind: releasecontract.RunnerLive, Name: target.ID},
			Attempt:       attempt,
			StartedAt:     now,
			FinishedAt:    now,
			Status:        status,
			Reason:        oneLine(reason),
		},
	}
}

func classifyFailure(err error) *Failure {
	if err == nil {
		return nil
	}
	var failure *Failure
	if errors.As(err, &failure) {
		switch failure.Status {
		case releasecontract.StatusExternalBlocked, releasecontract.StatusProductFailure:
		default:
			return &Failure{
				Status: releasecontract.StatusProductFailure,
				Reason: "adapter returned an unsupported failure classification",
			}
		}
		failure.Reason = oneLine(failure.Reason)
		if failure.Reason == "" {
			failure.Reason = "adapter returned an empty failure reason"
			failure.Status = releasecontract.StatusProductFailure
			failure.Retryable = false
		}
		return failure
	}
	return &Failure{
		Status: releasecontract.StatusProductFailure,
		Reason: oneLine(err.Error()),
	}
}

func mergeFailures(primary, cleanup *Failure) *Failure {
	if primary == nil {
		return cleanup
	}
	if cleanup == nil {
		return primary
	}
	status := primary.Status
	if cleanup.Status == releasecontract.StatusProductFailure {
		status = releasecontract.StatusProductFailure
	}
	return &Failure{
		Status:    status,
		Reason:    oneLine(primary.Reason + "; cleanup: " + cleanup.Reason),
		Retryable: primary.Retryable && cleanup.Retryable,
	}
}

func failureFromExecution(execution Execution) *Failure {
	if execution.Result.Status == releasecontract.StatusPass ||
		execution.Result.Status == releasecontract.StatusFlaky {
		return nil
	}
	for _, phase := range execution.Phases {
		if phase.Status == string(execution.Result.Status) {
			return &Failure{
				Status: execution.Result.Status,
				Reason: execution.Result.Reason,
				Retryable: execution.Result.Status == releasecontract.StatusExternalBlocked &&
					execution.Retryable,
			}
		}
	}
	return &Failure{Status: execution.Result.Status, Reason: execution.Result.Reason}
}

func containsSecret(value string, secrets map[string]string) bool {
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxReasonLength = 500
	if len(value) > maxReasonLength {
		return value[:maxReasonLength] + "..."
	}
	return value
}
