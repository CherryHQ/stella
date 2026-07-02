package reflect

import (
	"strings"
	"testing"
)

func TestFactUserRequiresSubjectFitThree(t *testing.T) {
	result := gateFactCandidates([]factCandidate{
		validFactCandidate("fact-0001", factSubjectUser),
	}, []factEvaluation{{
		Ref:    "fact-0001",
		Scores: factScores(4, 2, 4, 4, 4),
	}}, factGateOptions{PrivateOneToOne: true})

	assertSingleReject(t, result, "fact-0001", rejectScoreFloorFailed)
}

func TestFactAgentRequiresSubjectFitThree(t *testing.T) {
	result := gateFactCandidates([]factCandidate{
		validFactCandidate("fact-0001", factSubjectAgent),
	}, []factEvaluation{{
		Ref:    "fact-0001",
		Scores: factScores(4, 2, 4, 4, 4),
	}}, factGateOptions{PrivateOneToOne: true})

	assertSingleReject(t, result, "fact-0001", rejectScoreFloorFailed)
}

func TestFactWorldAcceptsSubjectFitTwo(t *testing.T) {
	result := gateFactCandidates([]factCandidate{
		validFactCandidate("fact-0001", factSubjectWorld),
	}, []factEvaluation{{
		Ref:    "fact-0001",
		Scores: factScores(4, 2, 4, 4, 4),
	}}, factGateOptions{PrivateOneToOne: false})

	if !equalRefs(gotRefs(result.Accepted), []CandidateRef{"fact-0001"}) {
		t.Fatalf("expected world fact accepted, got accepted=%#v rejected=%#v", result.Accepted, result.Rejected)
	}
}

func TestFactWorldRequiresKnowledgeSearchHint(t *testing.T) {
	candidate := validFactCandidate("fact-0001", factSubjectWorld)
	candidate.HandoffHints.KnowledgeSearchQueryHint = ""

	result := gateFactCandidates([]factCandidate{candidate}, []factEvaluation{{
		Ref:    "fact-0001",
		Scores: factScores(4, 4, 4, 4, 4),
	}}, factGateOptions{PrivateOneToOne: true})

	assertSingleReject(t, result, "fact-0001", rejectSchemaMissingField)
}

func TestFactSingletonSubjectsRejectKnowledgeSearchHint(t *testing.T) {
	user := validFactCandidate("fact-0001", factSubjectUser)
	user.HandoffHints.KnowledgeSearchQueryHint = "profile hint"
	agent := validFactCandidate("fact-0002", factSubjectAgent)
	agent.HandoffHints.KnowledgeSearchQueryHint = "soul hint"

	result := gateFactCandidates([]factCandidate{user, agent}, []factEvaluation{
		{Ref: "fact-0001", Scores: factScores(4, 4, 4, 4, 4)},
		{Ref: "fact-0002", Scores: factScores(4, 4, 4, 4, 4)},
	}, factGateOptions{PrivateOneToOne: true})

	if len(result.Accepted) != 0 {
		t.Fatalf("expected no accepted singleton hints, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 2 {
		t.Fatalf("expected two rejected singleton hints, got %#v", result.Rejected)
	}
	for _, decision := range result.Rejected {
		if decision.Reason != rejectSchemaMissingField {
			t.Fatalf("expected schema rejection, got %#v", result.Rejected)
		}
	}
}

func TestFactToolResultEvidenceRequiresReviewSummary(t *testing.T) {
	valid := validFactCandidate("fact-0001", factSubjectWorld)
	valid.Evidence = []factEvidence{{
		SourceType: factEvidenceToolResult,
		Source:     "[tool_result_summary] tool=shell call_id=tc1 detected project setting",
		Reason:     "The controlled tool summary reports the durable project setting.",
	}}
	invalid := validFactCandidate("fact-0002", factSubjectWorld)
	invalid.Evidence = []factEvidence{{
		SourceType: factEvidenceToolResult,
		Source:     "raw command output with no review summary marker",
		Reason:     "This uses a raw tool result.",
	}}

	result := gateFactCandidates([]factCandidate{valid, invalid}, []factEvaluation{
		{Ref: "fact-0001", Scores: factScores(4, 4, 4, 4, 4)},
		{Ref: "fact-0002", Scores: factScores(4, 4, 4, 4, 4)},
	}, factGateOptions{PrivateOneToOne: true})

	if !equalRefs(gotRefs(result.Accepted), []CandidateRef{"fact-0001"}) {
		t.Fatalf("expected only controlled tool summary accepted, got accepted=%#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Ref != "fact-0002" || result.Rejected[0].Reason != rejectSchemaMissingField {
		t.Fatalf("expected raw tool evidence rejected, got %#v", result.Rejected)
	}
}

func TestFactCapsArePerSubject(t *testing.T) {
	var candidates []factCandidate
	var evaluations []factEvaluation
	for _, subject := range []factSubject{factSubjectUser, factSubjectAgent, factSubjectWorld} {
		for i := range 4 {
			ref := candidateRef("fact", len(candidates))
			candidate := validFactCandidate(ref, subject)
			candidate.Content = candidate.Content + " " + string(rune('a'+i))
			candidates = append(candidates, candidate)
			evaluations = append(evaluations, factEvaluation{Ref: ref, Scores: factScores(4, 4, 4, 4, 4)})
		}
	}

	result := gateFactCandidates(candidates, evaluations, factGateOptions{PrivateOneToOne: true})

	if len(result.Accepted) != 9 {
		t.Fatalf("expected 9 accepted facts, got %d: %#v", len(result.Accepted), result.Accepted)
	}
	if len(result.Rejected) != 3 {
		t.Fatalf("expected 3 cap drops, got %#v", result.Rejected)
	}
	for _, decision := range result.Rejected {
		if decision.Reason != rejectCapDropped {
			t.Fatalf("expected cap drop, got %#v", result.Rejected)
		}
	}
}

func TestFactSkipsUserAgentForNonOneToOne(t *testing.T) {
	result := gateFactCandidates([]factCandidate{
		validFactCandidate("fact-0001", factSubjectUser),
		validFactCandidate("fact-0002", factSubjectAgent),
		validFactCandidate("fact-0003", factSubjectWorld),
	}, []factEvaluation{
		{Ref: "fact-0001", Scores: factScores(4, 4, 4, 4, 4)},
		{Ref: "fact-0002", Scores: factScores(4, 4, 4, 4, 4)},
		{Ref: "fact-0003", Scores: factScores(4, 4, 4, 4, 4)},
	}, factGateOptions{PrivateOneToOne: false})

	if !equalRefs(gotRefs(result.Accepted), []CandidateRef{"fact-0003"}) {
		t.Fatalf("expected only world fact accepted, got accepted=%#v rejected=%#v", result.Accepted, result.Rejected)
	}
	for _, decision := range result.Rejected {
		if decision.Reason != rejectScopeNotEligible {
			t.Fatalf("expected scope rejection, got %#v", result.Rejected)
		}
	}
}

func TestFactPromptsDocumentFreshOnlyAndNoWrites(t *testing.T) {
	generationChecks := []string{
		"fresh_conversation",
		"prior_context is only for disambiguation",
		"Read the full bounded review context",
		"submit_fact_candidate",
		"finish_fact_generation",
		"Do not call profile_update",
		"Do not call soul_update",
		"Do not write facts",
	}
	for _, want := range generationChecks {
		if !strings.Contains(factCandidateGenerationPrompt, want) {
			t.Fatalf("fact generation prompt missing %q", want)
		}
	}

	evaluationChecks := []string{
		"candidate_ref",
		"Do not modify candidate content",
		"Do not output overall",
		"Do not output passes_threshold",
		"Do not write facts",
	}
	for _, want := range evaluationChecks {
		if !strings.Contains(factCandidateEvaluationPrompt, want) {
			t.Fatalf("fact evaluation prompt missing %q", want)
		}
	}
}

func validFactCandidate(ref CandidateRef, subject factSubject) factCandidate {
	candidate := factCandidate{
		Ref:            ref,
		Subject:        subject,
		Content:        "The user wants durable fact candidates to stay separate from skills.",
		Evidence:       []factEvidence{{SourceType: factEvidenceUserMessage, Source: "user stated the boundary", Reason: "This is direct fresh user evidence."}},
		ExpectedEffect: "Future reflect work should preserve this memory boundary.",
	}
	if subject == factSubjectWorld {
		candidate.HandoffHints.KnowledgeSearchQueryHint = "reflect fact skill boundary"
	}
	return candidate
}

func factScores(evidence, subject, durability, utility, atomicity int) map[string]int {
	return map[string]int{
		factScoreEvidenceStrength: evidence,
		factScoreSubjectFit:       subject,
		factScoreDurability:       durability,
		factScoreFutureUtility:    utility,
		factScoreAtomicity:        atomicity,
	}
}

func assertSingleReject(t *testing.T, result CandidateGateResult, ref CandidateRef, reason RejectReason) {
	t.Helper()
	if len(result.Accepted) != 0 {
		t.Fatalf("expected no accepted candidates, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 {
		t.Fatalf("expected one rejected candidate, got %#v", result.Rejected)
	}
	if result.Rejected[0].Ref != ref || result.Rejected[0].Reason != reason {
		t.Fatalf("expected rejection %s/%s, got %#v", ref, reason, result.Rejected)
	}
}
