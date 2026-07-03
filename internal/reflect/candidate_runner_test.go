package reflect

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func TestFactCandidateRunnerGeneratesEvaluatesAndGates(t *testing.T) {
	stream := sequentialCaptureStream(t,
		[]ai.ToolCall{
			rawToolCall("submit_fact_candidate", `{
				"subject":"world",
				"content":"Reflect candidate generation is split from reconciliation.",
				"evidence":[{"source_type":"user_message","source":"[user] split #532 from #531","reason":"The user explicitly described the boundary."}],
				"expected_effect":"Future reflect work should preserve the #532/#531 boundary.",
				"handoff_hints":{"knowledge_search_query_hint":"reflect candidate generation reconciliation"}
			}`),
			rawToolCall("finish_fact_generation", `{"candidate_count":1,"reason":"one durable world fact"}`),
		},
		[]ai.ToolCall{
			rawToolCall("submit_fact_evaluation", `{
				"candidate_ref":"fact-0001",
				"scores":{"evidence_strength":4,"subject_fit":4,"durability":4,"future_utility":4,"atomicity":4},
				"rationale":"clear durable project fact"
			}`),
			rawToolCall("finish_fact_evaluation", `{"candidate_count":1,"reason":"complete"}`),
		},
	)
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}

	got, err := runner.runFactLine(context.Background(), ReviewUnit{
		Text:            "<fresh_conversation>\n[user] split #532 from #531\n</fresh_conversation>\n",
		FreshCount:      1,
		PrivateOneToOne: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one accepted fact candidate, got %#v", got)
	}
	if got[0].Ref != "fact-0001" {
		t.Fatalf("expected host-assigned ref, got %#v", got[0])
	}
}

func TestFactCandidateRunnerAccumulatesStreamedToolArguments(t *testing.T) {
	stream := sequentialChunkedCaptureStream(t,
		[]chunkedToolCall{
			chunkedRawToolCall("submit_fact_candidate", []string{
				`{"subject":"world","content":"`,
				`Streamed tool arguments must be accumulated.","evidence":[{"source_type":"user_message","source":"[user] split args","reason":"The user provided the signal."}],`,
				`"expected_effect":"Capture works with real streamed providers.","handoff_hints":{"knowledge_search_query_hint":"streamed tool arguments"}}`,
			}),
			chunkedRawToolCall("finish_fact_generation", []string{`{"candidate_count":1,"reason":"one candidate"}`}),
		},
		[]chunkedToolCall{
			chunkedRawToolCall("submit_fact_evaluation", []string{
				`{"candidate_ref":"fact-0001",`,
				`"scores":{"evidence_strength":4,"subject_fit":4,"durability":4,"future_utility":4,"atomicity":4},`,
				`"rationale":"clear durable signal"}`,
			}),
			chunkedRawToolCall("finish_fact_evaluation", []string{`{"candidate_count":1,"reason":"complete"}`}),
		},
	)
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}

	got, err := runner.runFactLine(context.Background(), ReviewUnit{
		Text:            "<fresh_conversation>\n[user] split args\n</fresh_conversation>\n",
		FreshCount:      1,
		PrivateOneToOne: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one accepted fact candidate, got %#v", got)
	}
}

func TestSkillCandidateRunnerGeneratesEvaluatesAndGates(t *testing.T) {
	stream := sequentialCaptureStream(t,
		[]ai.ToolCall{
			rawToolCall("submit_skill_candidate", `{
				"learning":{"summary":"Use ReviewUnit windows before reflecting.","reusable_delta":"Bounded windows prevent silently dropping long fresh content."},
				"evidence":[{"signal_type":"successful_workflow","source":"[assistant] built ReviewUnit first","reason":"The workflow generalized to reflect development."}],
				"applicability":{"trigger_examples":["Implementing a reflect reviewer"],"non_trigger_examples":["Answering a one-off question"]},
				"procedure":{"prerequisites":["Need access to session history"],"steps":["Build bounded review context","Run candidate generation","Run evaluation"],"decision_points":["Skip when there is no fresh content"],"pitfalls":["Do not use prior_context as evidence"],"verification":["Run reflect package tests"]},
				"handoff_hints":{"search_query_hint":"reflect bounded review candidate pipeline"}
			}`),
			rawToolCall("finish_skill_generation", `{"candidate_count":1,"reason":"one reusable workflow"}`),
		},
		[]ai.ToolCall{
			rawToolCall("submit_skill_evaluation", `{
				"candidate_ref":"skill-0001",
				"scores":{"evidence_strength":4,"reusable_value":4,"baseline_separation":4,"procedure_actionability":4,"applicability_clarity":3,"verification_quality":3},
				"rationale":"clear reusable workflow"
			}`),
			rawToolCall("finish_skill_evaluation", `{"candidate_count":1,"reason":"complete"}`),
		},
	)
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}

	got, err := runner.runSkillLine(context.Background(), ReviewUnit{
		Text:            "<fresh_conversation>\n[assistant] built ReviewUnit first\n</fresh_conversation>\n",
		FreshCount:      1,
		PrivateOneToOne: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one accepted skill candidate, got %#v", got)
	}
	if got[0].Ref != "skill-0001" {
		t.Fatalf("expected host-assigned ref, got %#v", got[0])
	}
}

type chunkedToolCall struct {
	ID     string
	Name   string
	Chunks []string
}

func sequentialCaptureStream(t *testing.T, callsByRequest ...[]ai.ToolCall) providers.StreamFunc {
	t.Helper()
	request := 0
	return func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if len(aiCtx.Tools) == 0 {
			t.Fatalf("request %d exposed no capture tools", request)
		}
		if request >= len(callsByRequest) {
			t.Fatalf("unexpected provider request %d with system %q", request, aiCtx.System)
		}
		stream := providers.NewChannelEventStream(16)
		for _, call := range callsByRequest[request] {
			stream.Emit(ai.EventToolCallDelta{ID: call.ID, Name: call.Name, Arguments: call.Arguments["raw"].(string)})
		}
		stream.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
		stream.Finish(nil)
		request++
		return stream, nil
	}
}

func sequentialChunkedCaptureStream(t *testing.T, callsByRequest ...[]chunkedToolCall) providers.StreamFunc {
	t.Helper()
	request := 0
	return func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if len(aiCtx.Tools) == 0 {
			t.Fatalf("request %d exposed no capture tools", request)
		}
		if request >= len(callsByRequest) {
			t.Fatalf("unexpected provider request %d with system %q", request, aiCtx.System)
		}
		stream := providers.NewChannelEventStream(16)
		for _, call := range callsByRequest[request] {
			stream.Emit(ai.EventToolCallDelta{ID: call.ID, Name: call.Name})
			for _, chunk := range call.Chunks {
				stream.Emit(ai.EventToolCallDelta{ID: call.ID, Arguments: chunk})
			}
		}
		stream.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
		stream.Finish(nil)
		request++
		return stream, nil
	}
}

func rawToolCall(name, raw string) ai.ToolCall {
	return ai.ToolCall{
		ID:        strings.ReplaceAll(name, "submit_", "call-"),
		Name:      name,
		Arguments: map[string]any{"raw": raw},
	}
}

func chunkedRawToolCall(name string, chunks []string) chunkedToolCall {
	return chunkedToolCall{
		ID:     strings.ReplaceAll(name, "submit_", "call-"),
		Name:   name,
		Chunks: chunks,
	}
}
