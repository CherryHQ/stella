package reflect

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	testSubmitScore = "submit_score"
	testSubmitBatch = "submit_batch"
	testFinish      = "finish_capture"
)

func TestCaptureSingleBatchSubmitPassesWithoutFinish(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{batchSubmitCall("batch-1")},
	}, testBatchCaptureProtocol())
	if err != nil {
		t.Fatalf("expected a single batch submit tool to complete capture, got %v", err)
	}
}

func TestCaptureMissingBatchSubmitFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{},
	}, testBatchCaptureProtocol())
	if err == nil {
		t.Fatal("expected missing batch submit to fail closed")
	}
}

func TestCaptureDuplicateBatchSubmitFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{
			batchSubmitCall("batch-1"),
			batchSubmitCall("batch-2"),
		},
	}, testBatchCaptureProtocol())
	if err == nil {
		t.Fatal("expected duplicate batch submit to fail closed")
	}
}

func TestCaptureUnknownCandidateRefFailsClosed(t *testing.T) {
	err := validateCaptureProtocol(captureRunResult{
		ToolCalls: []ai.ToolCall{
			scoreCall("fact-9999"),
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
		},
	}, testBatchCaptureProtocol())
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
				return captureRunResult{ToolCalls: []ai.ToolCall{}}, nil
			}
			return captureRunResult{ToolCalls: []ai.ToolCall{batchSubmitCall("batch-1")}}, nil
		},
		func(result captureRunResult) error {
			return validateCaptureProtocol(result, testBatchCaptureProtocol())
		},
	)
	if err != nil {
		t.Fatalf("expected retry to recover, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
	if len(result.ToolCalls) != 1 {
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

func TestDecodeCaptureArgsParsesRawJSON(t *testing.T) {
	args, err := captureCallArguments(ai.ToolCall{
		ID:   "call-1",
		Name: testSubmitScore,
		Arguments: map[string]any{
			"raw": `{"candidate_ref":"fact-0001","scores":{"evidence_strength":4},"rationale":"clear"}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if args["candidate_ref"] != "fact-0001" {
		t.Fatalf("expected parsed candidate_ref, got %#v", args)
	}
	scores, ok := args["scores"].(map[string]any)
	if !ok || scores["evidence_strength"].(float64) != 4 {
		t.Fatalf("expected parsed nested scores, got %#v", args["scores"])
	}
}

func TestDecodeCapturePayloadRejectsUnknownFields(t *testing.T) {
	_, err := decodeCapturePayload[factCandidate](ai.ToolCall{
		ID:   "call-1",
		Name: toolSubmitFactGeneration,
		Arguments: map[string]any{
			"raw": `{
				"subject":"world",
				"content":"A durable project fact.",
				"evidence":[{"source_type":"user_message","source":"[user] durable fact","reason":"explicitly stated"}],
				"expected_effect":"Use this when relevant later.",
				"handoff_hints":{"knowledge_search_query_hint":"durable project fact"},
				"confidence":0.9
			}`,
		},
	})
	if err == nil {
		t.Fatal("expected unknown payload field to fail closed")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func testCaptureProtocol(refs ...CandidateRef) captureProtocol {
	return captureProtocol{
		AllowedTools: map[string]struct{}{
			testSubmitScore: {},
		},
		SubmitName:     testSubmitScore,
		EvaluationName: testSubmitScore,
		ExpectedRefs:   refs,
	}
}

func testBatchCaptureProtocol() captureProtocol {
	return captureProtocol{
		AllowedTools: map[string]struct{}{
			testSubmitBatch: {},
		},
		SubmitName: testSubmitBatch,
	}
}

func scoreCall(ref CandidateRef) ai.ToolCall {
	return ai.ToolCall{
		ID:        "score-" + string(ref),
		Name:      testSubmitScore,
		Arguments: map[string]any{"candidate_ref": string(ref)},
	}
}

func batchSubmitCall(id string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: testSubmitBatch, Arguments: map[string]any{}}
}
