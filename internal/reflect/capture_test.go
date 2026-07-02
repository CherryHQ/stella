package reflect

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	testSubmitScore = "submit_score"
	testFinish      = "finish_capture"
)

func TestCaptureMissingFinishFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{scoreCall("fact-0001")},
	}, testCaptureProtocol("fact-0001"))
	if err == nil {
		t.Fatal("expected missing finish to fail closed")
	}
}

func TestCaptureDuplicateFinishFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{
			scoreCall("fact-0001"),
			finishCall("finish-1"),
			finishCall("finish-2"),
		},
	}, testCaptureProtocol("fact-0001"))
	if err == nil {
		t.Fatal("expected duplicate finish to fail closed")
	}
}

func TestCaptureUnknownCandidateRefFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{
			scoreCall("fact-9999"),
			finishCall("finish-1"),
		},
	}, testCaptureProtocol("fact-0001"))
	if err == nil {
		t.Fatal("expected unknown candidate_ref to fail closed")
	}
}

func TestCaptureMissingEvaluationFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{
			scoreCall("fact-0001"),
			finishCall("finish-1"),
		},
	}, testCaptureProtocol("fact-0001", "fact-0002"))
	if err == nil {
		t.Fatal("expected missing evaluation to fail closed")
	}
}

func TestCaptureUnknownToolFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{
			{Name: "unexpected_tool", Arguments: map[string]any{}},
			finishCall("finish-1"),
		},
	}, testCaptureProtocol())
	if err == nil {
		t.Fatal("expected unknown capture tool to fail closed")
	}
}

func TestCaptureRetriesOnceOnProtocolFailure(t *testing.T) {
	attempts := 0
	result, err := runCaptureWithRetry(context.Background(),
		func(context.Context) (captureRunResult, error) {
			attempts++
			if attempts == 1 {
				return captureRunResult{ToolCalls: []ai.ToolCall{scoreCall("fact-0001")}}, nil
			}
			return captureRunResult{ToolCalls: []ai.ToolCall{scoreCall("fact-0001"), finishCall("finish-1")}}, nil
		},
		func(result captureRunResult) error {
			return validateCaptureProtocol(result, testCaptureProtocol("fact-0001"))
		},
	)
	if err != nil {
		t.Fatalf("expected retry to recover, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected second result, got %#v", result)
	}
}

func TestCaptureDoesNotRetryRunnerError(t *testing.T) {
	attempts := 0
	wantErr := errors.New("provider unavailable")
	_, err := runCaptureWithRetry(context.Background(),
		func(context.Context) (captureRunResult, error) {
			attempts++
			return captureRunResult{}, wantErr
		},
		func(captureRunResult) error { return nil },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected runner error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected no protocol retry for runner error, got %d attempts", attempts)
	}
}

func testCaptureProtocol(refs ...CandidateRef) captureProtocol {
	return captureProtocol{
		AllowedTools: map[string]struct{}{
			testSubmitScore: {},
			testFinish:      {},
		},
		FinishName:     testFinish,
		EvaluationName: testSubmitScore,
		ExpectedRefs:   refs,
	}
}

func scoreCall(ref CandidateRef) ai.ToolCall {
	return ai.ToolCall{
		ID:        "score-" + string(ref),
		Name:      testSubmitScore,
		Arguments: map[string]any{"candidate_ref": string(ref)},
	}
}

func finishCall(id string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: testFinish, Arguments: map[string]any{}}
}
