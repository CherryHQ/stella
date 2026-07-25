package groupingest

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func TestGroupCandidateReviewerGeneratesEvaluatesAndGates(t *testing.T) {
	stream := sequentialGroupCaptureStream(t,
		groupRawToolCall(toolSubmitGroupFactGeneration, `{"candidates":[{
			"subject":"human",
			"subject_ref":"subject-0001",
			"content":"Owns production release coordination.",
			"evidence":[{"source":"Alice will remain responsible for production releases.","reason":"A human explicitly assigned an enduring responsibility."}],
			"expected_effect":"Future release questions route to the correct owner."
		}]}`),
		groupRawToolCall(toolSubmitGroupFactEvaluations, `{"evaluations":[{
			"candidate_ref":"group-fact-0001",
			"scores":{"evidence_strength":4,"subject_fit":4,"durability":4,"future_utility":4,"atomicity":4},
			"rationale":"Explicit durable collaboration ownership."
		}]}`),
	)
	reviewer := CandidateReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}
	unit := GroupReviewUnit{
		GroupID: "group-1",
		Text:    "<fresh_public_messages>[]</fresh_public_messages>",
		Subjects: map[string]GroupSubjectCatalogEntry{
			"subject-0001": {Ref: "subject-0001", Subject: "human", SubjectID: "alice", DisplayName: "Alice"},
		},
	}
	result, err := reviewer.Run(context.Background(), unit)
	if err != nil {
		t.Fatalf("review candidates: %v", err)
	}
	if len(result.Accepted) != 1 || result.Accepted[0].Ref != "group-fact-0001" {
		t.Fatalf("accepted = %#v", result.Accepted)
	}
}

func TestGroupCandidateReviewerZeroCandidateSkipsEvaluator(t *testing.T) {
	calls := 0
	stream := sequentialGroupCaptureStream(t,
		groupRawToolCall(toolSubmitGroupFactGeneration, `{"candidates":[],"no_candidate_reason":"Only a temporary ticket status was discussed."}`),
	)
	reviewer := CandidateReviewer{
		Stream: stream,
		Model:  ai.Model{ID: "test-model"},
		OnGenerated: func(count int) {
			calls++
			if count != 0 {
				t.Fatalf("generated count = %d", count)
			}
		},
	}
	result, err := reviewer.Run(context.Background(), GroupReviewUnit{GroupID: "group-1", Text: "temporary status"})
	if err != nil {
		t.Fatalf("review candidates: %v", err)
	}
	if len(result.Generated) != 0 || len(result.Accepted) != 0 || calls != 1 {
		t.Fatalf("result=%#v generated callbacks=%d", result, calls)
	}
}

func TestGroupCandidateGateRequiresEveryScoreAtLeastThreeAndAveragePointEight(t *testing.T) {
	candidate := GroupFactCandidate{
		Ref:            "group-fact-0001",
		Subject:        "group",
		Content:        "Production releases require two approvals.",
		Evidence:       []GroupFactEvidence{{Source: "We always require two approvals.", Reason: "Explicit group rule."}},
		ExpectedEffect: "Future release checks apply the durable rule.",
	}
	allThree := GroupFactEvaluation{
		Ref: candidate.Ref,
		Scores: map[string]int{
			groupScoreEvidenceStrength: 3,
			groupScoreSubjectFit:       3,
			groupScoreDurability:       3,
			groupScoreFutureUtility:    3,
			groupScoreAtomicity:        3,
		},
		Rationale: "Clear but not exceptional.",
	}
	if got := gateGroupFactCandidates([]GroupFactCandidate{candidate}, []GroupFactEvaluation{allThree}, GroupCandidateGateSettings{}); len(got.Accepted) != 0 {
		t.Fatalf("all-three candidate should miss the 0.80 average: %#v", got)
	}
	allThree.Scores[groupScoreEvidenceStrength] = 4
	if got := gateGroupFactCandidates([]GroupFactCandidate{candidate}, []GroupFactEvaluation{allThree}, GroupCandidateGateSettings{}); len(got.Accepted) != 1 {
		t.Fatalf("3.2 average candidate should pass: %#v", got)
	}
	allThree.Scores[groupScoreAtomicity] = 2
	allThree.Scores[groupScoreSubjectFit] = 4
	if got := gateGroupFactCandidates([]GroupFactCandidate{candidate}, []GroupFactEvaluation{allThree}, GroupCandidateGateSettings{}); len(got.Accepted) != 0 {
		t.Fatalf("score below core floor should fail despite average: %#v", got)
	}
}

func TestGroupCandidateGateUsesConfiguredWeightsFloorThresholdAndCap(t *testing.T) {
	candidates := []GroupFactCandidate{
		{Ref: "group-fact-0001", Subject: "group", Content: "Rule one."},
		{Ref: "group-fact-0002", Subject: "group", Content: "Rule two."},
	}
	evaluations := []GroupFactEvaluation{
		{
			Ref: candidates[0].Ref,
			Scores: map[string]int{
				groupScoreEvidenceStrength: 4,
				groupScoreSubjectFit:       2,
				groupScoreDurability:       2,
				groupScoreFutureUtility:    2,
				groupScoreAtomicity:        2,
			},
		},
		{
			Ref: candidates[1].Ref,
			Scores: map[string]int{
				groupScoreEvidenceStrength: 3,
				groupScoreSubjectFit:       4,
				groupScoreDurability:       4,
				groupScoreFutureUtility:    4,
				groupScoreAtomicity:        4,
			},
		},
	}
	settings := GroupCandidateGateSettings{
		Weights:      map[string]float64{groupScoreEvidenceStrength: 1},
		CoreFloor:    2,
		Threshold:    0.75,
		CandidateCap: 1,
	}

	got := gateGroupFactCandidates(candidates, evaluations, settings)
	if len(got.Accepted) != 1 || got.Accepted[0].Ref != candidates[0].Ref {
		t.Fatalf("configured gate accepted = %#v", got.Accepted)
	}
	if len(got.Rejected) != 1 || got.Rejected[0].Ref != candidates[1].Ref {
		t.Fatalf("configured gate rejected = %#v", got.Rejected)
	}
}

func TestGroupGenerationContractUsesConfiguredCapAndResolvableSubjects(t *testing.T) {
	const cap = 2
	prompt := renderGroupFactGenerationPrompt(cap)
	for _, required := range []string{
		"Return at most 2 candidates",
		"valid subject_ref in the supplied review context",
		"an unresolved name, a named outsider, or anyone absent",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("generation prompt missing %q", required)
		}
	}

	tools := groupFactGenerationTools(cap)
	properties := tools[0].InputSchema["properties"].(map[string]any)
	candidatesSchema := properties["candidates"].(map[string]any)
	if got := candidatesSchema["maxItems"]; got != cap {
		t.Fatalf("candidate schema maxItems = %v, want %d", got, cap)
	}

	unit := GroupReviewUnit{GroupID: "group-1"}
	tooMany := make([]GroupFactCandidate, cap+1)
	if err := validateGeneratedGroupCandidates(tooMany, unit, cap); err == nil {
		t.Fatal("configured candidate cap should be enforced by host validation")
	}
}

func TestGroupCandidatePromptsDefineHighDensitySelectionAndScoring(t *testing.T) {
	generationPrompt := strings.Join(strings.Fields(groupFactGenerationPromptTemplate), " ")
	generationChecks := []string{
		"Split independent rules, responsibilities",
		"Never satisfy the candidate cap by bundling independent facts",
		"explicit handoff from one known participant to another",
		"emit two separate candidates",
		"explicitly ends one known participant's durable",
		"never restate the old positive",
		"one-off instruction, approval, or exercise of authority",
		"do not generalize",
		"Do not prefer a candidate only because it appears later",
		"omit no_candidate_reason entirely",
	}
	for _, phrase := range generationChecks {
		if !strings.Contains(generationPrompt, phrase) {
			t.Fatalf("generation prompt missing %q", phrase)
		}
	}

	evaluationPrompt := strings.Join(strings.Fields(groupFactEvaluationPrompt), " ")
	evaluationChecks := []string{
		"General 0-4 scoring scale",
		"Only messages inside <fresh_public_messages>",
		"never counts as evidence",
		"generator summary, not an independent source",
		"Do not collapse scores 2 and 3",
		"some wording overreaches",
		"No individual score decides overall acceptance",
		"Reserve scores 3",
		"Group Fact rubric boundary",
		"Group collaboration scope is mandatory",
		"score at most 1 for subject_fit and future_utility",
		"Durability must outlive the current work item",
		"still task-scoped",
		"eligible reconciliation input",
		"does not by itself prove standing authority",
		"does not by itself establish a group-wide policy",
	}
	for _, phrase := range evaluationChecks {
		if !strings.Contains(evaluationPrompt, phrase) {
			t.Fatalf("evaluation prompt missing %q", phrase)
		}
	}
}

func TestValidateGeneratedGroupCandidateRejectsTemporarySubjectRefInContent(t *testing.T) {
	unit := GroupReviewUnit{
		Subjects: map[string]GroupSubjectCatalogEntry{
			"subject-0001": {
				Ref:       "subject-0001",
				Subject:   memory.GroupFactSubjectHuman,
				SubjectID: "alice",
			},
		},
	}
	candidate := GroupFactCandidate{
		Subject:        memory.GroupFactSubjectGroup,
		Content:        "subject-0001 owns production releases.",
		Evidence:       []GroupFactEvidence{{Source: "fresh message", Reason: "explicit statement"}},
		ExpectedEffect: "Route future release questions.",
	}

	if err := validateGeneratedGroupCandidate(candidate, unit); err == nil {
		t.Fatal("temporary subject_ref in candidate content should fail")
	}
}

func TestValidateGeneratedGroupCandidateRejectsUnknownParticipant(t *testing.T) {
	unit := GroupReviewUnit{
		Subjects: map[string]GroupSubjectCatalogEntry{
			"subject-0001": {
				Ref:       "subject-0001",
				Subject:   memory.GroupFactSubjectHuman,
				SubjectID: "alice",
			},
		},
	}
	candidate := GroupFactCandidate{
		Subject:        memory.GroupFactSubjectHuman,
		SubjectRef:     "subject-9999",
		Content:        "Owns production release coordination.",
		Evidence:       []GroupFactEvidence{{Source: "fresh message", Reason: "explicit statement"}},
		ExpectedEffect: "Route future release questions.",
	}

	if err := validateGeneratedGroupCandidate(candidate, unit); err == nil {
		t.Fatal("participant absent from the subject catalog should fail closed")
	}
}

func sequentialGroupCaptureStream(t *testing.T, calls ...ai.ToolCall) providers.StreamFunc {
	t.Helper()
	request := 0
	return func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		if request >= len(calls) {
			t.Fatalf("unexpected provider request %d", request)
		}
		if len(aiCtx.Tools) != 1 {
			t.Fatalf("request %d tools = %d, want 1", request, len(aiCtx.Tools))
		}
		call := calls[request]
		request++
		stream := providers.NewChannelEventStream(4)
		stream.Emit(ai.EventToolCallDelta{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments["raw"].(string),
		})
		stream.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
		stream.Finish(nil)
		return stream, nil
	}
}

func groupRawToolCall(name, raw string) ai.ToolCall {
	return ai.ToolCall{
		ID:        strings.ReplaceAll(name, "submit_", "call-"),
		Name:      name,
		Arguments: map[string]any{"raw": raw},
	}
}
