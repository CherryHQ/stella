package reflect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	candidateRefArgument = "candidate_ref"
	maxCaptureAttempts   = 3
)

var errCaptureProtocol = errors.New("capture protocol")

type captureRunResult struct {
	ToolCalls []ai.ToolCall
}

type captureProtocol struct {
	AllowedTools       map[string]struct{}
	SubmitName         string
	EvaluationName     string
	ExpectedRefs       []CandidateRef
	PayloadsValidator  func([]ai.ToolCall) error
	RepairRetries      bool
	RepairInstructions []string
}

// StructuredCaptureResult and StructuredCaptureProtocol expose the tested
// capture contract without exposing any 1v1 Fact or Skill schema.
type (
	StructuredCaptureResult   = captureRunResult
	StructuredCaptureProtocol = captureProtocol
)

type (
	captureAttemptFunc func(context.Context) (captureRunResult, error)
	captureValidator   func(captureRunResult) error
)

func runCaptureWithRetry(ctx context.Context, attempt captureAttemptFunc, validate captureValidator) (captureRunResult, error) {
	return runCaptureWithRetryLimit(ctx, maxCaptureAttempts, attempt, validate)
}

func runCaptureWithRetryLimit(ctx context.Context, maxAttempts int, attempt captureAttemptFunc, validate captureValidator) (captureRunResult, error) {
	if attempt == nil {
		return captureRunResult{}, fmt.Errorf("%w: attempt function missing", errCaptureProtocol)
	}
	if maxAttempts < 1 {
		return captureRunResult{}, fmt.Errorf("%w: max attempts must be positive", errCaptureProtocol)
	}
	if validate == nil {
		validate = func(captureRunResult) error { return nil }
	}

	var last captureRunResult
	var lastErr error
	for i := range maxAttempts {
		result, err := attempt(ctx)
		if err != nil {
			if errors.Is(err, errCaptureProtocol) {
				lastErr = err
				if i+1 < maxAttempts {
					continue
				}
			}
			return result, err
		}
		last = result
		if err := validate(result); err != nil {
			lastErr = err
			if i+1 < maxAttempts {
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
	if err := validateSingleSubmit(result.ToolCalls, protocol.SubmitName); err != nil {
		return err
	}
	if err := validateExpectedCandidateRefs(result.ToolCalls, protocol.EvaluationName, protocol.ExpectedRefs); err != nil {
		return err
	}
	if protocol.PayloadsValidator != nil {
		return protocol.PayloadsValidator(result.ToolCalls)
	}
	return nil
}

func validateSingleSubmit(calls []ai.ToolCall, submitName string) error {
	if submitName == "" {
		return fmt.Errorf("%w: submit tool name missing", errCaptureProtocol)
	}
	count := 0
	for _, call := range calls {
		if call.Name == submitName {
			count++
		}
	}
	switch {
	case count == 0:
		return fmt.Errorf("%w: missing submit tool %q", errCaptureProtocol, submitName)
	case count > 1:
		return fmt.Errorf("%w: duplicate submit tool %q", errCaptureProtocol, submitName)
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

func normalizeCaptureToolCalls(calls []ai.ToolCall) ([]ai.ToolCall, error) {
	normalized := make([]ai.ToolCall, 0, len(calls))
	for _, call := range calls {
		args, err := captureCallArguments(call)
		if err != nil {
			return nil, err
		}
		call.Arguments = args
		normalized = append(normalized, call)
	}
	return normalized, nil
}

// captureCallArguments accepts both already-parsed provider arguments and the
// raw JSON form returned by providers.Complete for streamed tool-call deltas.
func captureCallArguments(call ai.ToolCall) (map[string]any, error) {
	if call.Arguments == nil {
		return map[string]any{}, nil
	}
	raw, _ := call.Arguments["raw"].(string)
	if strings.TrimSpace(raw) == "" {
		return call.Arguments, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("%w: decode raw arguments for %q: %w", errCaptureProtocol, call.Name, err)
	}
	return args, nil
}

func decodeCapturePayload[T any](call ai.ToolCall) (T, error) {
	var payload T
	args, err := captureCallArguments(call)
	if err != nil {
		return payload, err
	}
	data, err := json.Marshal(args)
	if err != nil {
		return payload, fmt.Errorf("%w: encode arguments for %q: %w", errCaptureProtocol, call.Name, err)
	}
	// Capture payloads are host contracts; model-created fields must fail closed.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("%w: decode arguments for %q: %w", errCaptureProtocol, call.Name, err)
	}
	return payload, nil
}

// DecodeStructuredCapturePayload strictly decodes one model tool call and
// rejects unknown fields.
func DecodeStructuredCapturePayload[T any](call ai.ToolCall) (T, error) {
	return decodeCapturePayload[T](call)
}

// AllowedCaptureTools builds the allowlist for one capture phase.
func AllowedCaptureTools(names ...string) map[string]struct{} {
	return allowedCaptureTools(names...)
}
