package reflect

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	candidateRefArgument = "candidate_ref"
	maxCaptureAttempts   = 2
)

var errCaptureProtocol = errors.New("capture protocol")

type captureRunResult struct {
	ToolCalls  []ai.ToolCall
	FinishSeen bool
}

type captureProtocol struct {
	AllowedTools   map[string]struct{}
	FinishName     string
	EvaluationName string
	ExpectedRefs   []CandidateRef
}

type (
	captureAttemptFunc func(context.Context) (captureRunResult, error)
	captureValidator   func(captureRunResult) error
)

func runCaptureWithRetry(ctx context.Context, attempt captureAttemptFunc, validate captureValidator) (captureRunResult, error) {
	if attempt == nil {
		return captureRunResult{}, fmt.Errorf("%w: attempt function missing", errCaptureProtocol)
	}
	if validate == nil {
		validate = func(captureRunResult) error { return nil }
	}

	var last captureRunResult
	var lastErr error
	for i := range maxCaptureAttempts {
		result, err := attempt(ctx)
		if err != nil {
			return result, err
		}
		last = result
		if err := validate(result); err != nil {
			lastErr = err
			if i+1 < maxCaptureAttempts {
				continue
			}
			return last, lastErr
		}
		return result, nil
	}
	return last, lastErr
}

func validateCaptureProtocol(result captureRunResult, protocol captureProtocol) error {
	if err := rejectUnknownCaptureTools(result.ToolCalls, protocol.AllowedTools); err != nil {
		return err
	}
	if err := validateSingleFinish(result.ToolCalls, protocol.FinishName); err != nil {
		return err
	}
	if err := validateExpectedCandidateRefs(result.ToolCalls, protocol.EvaluationName, protocol.ExpectedRefs); err != nil {
		return err
	}
	return nil
}

func validateSingleFinish(calls []ai.ToolCall, finishName string) error {
	if finishName == "" {
		return fmt.Errorf("%w: finish tool name missing", errCaptureProtocol)
	}
	count := 0
	for _, call := range calls {
		if call.Name == finishName {
			count++
		}
	}
	switch {
	case count == 0:
		return fmt.Errorf("%w: missing finish tool %q", errCaptureProtocol, finishName)
	case count > 1:
		return fmt.Errorf("%w: duplicate finish tool %q", errCaptureProtocol, finishName)
	default:
		return nil
	}
}

func rejectUnknownCaptureTools(calls []ai.ToolCall, allowed map[string]struct{}) error {
	for _, call := range calls {
		if _, ok := allowed[call.Name]; !ok {
			return fmt.Errorf("%w: unknown capture tool %q", errCaptureProtocol, call.Name)
		}
	}
	return nil
}

func validateExpectedCandidateRefs(calls []ai.ToolCall, evaluationName string, expectedRefs []CandidateRef) error {
	if evaluationName == "" {
		return nil
	}

	expected := make(map[CandidateRef]struct{}, len(expectedRefs))
	for _, ref := range expectedRefs {
		expected[ref] = struct{}{}
	}

	seen := make(map[CandidateRef]struct{}, len(expectedRefs))
	for _, call := range calls {
		if call.Name != evaluationName {
			continue
		}
		refText, ok := call.Arguments[candidateRefArgument].(string)
		if !ok || refText == "" {
			return fmt.Errorf("%w: evaluation call %q missing candidate_ref", errCaptureProtocol, call.ID)
		}
		ref := CandidateRef(refText)
		if _, ok := expected[ref]; !ok {
			return fmt.Errorf("%w: unknown candidate_ref %q", errCaptureProtocol, ref)
		}
		seen[ref] = struct{}{}
	}

	for _, ref := range expectedRefs {
		if _, ok := seen[ref]; !ok {
			return fmt.Errorf("%w: missing evaluation for candidate_ref %q", errCaptureProtocol, ref)
		}
	}
	return nil
}
