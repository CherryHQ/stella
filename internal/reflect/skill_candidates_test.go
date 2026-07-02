package reflect

import (
	"strings"
	"testing"
)

func TestSkillCandidateRejectsLoadedSkillEvidenceSignal(t *testing.T) {
	candidate := validSkillCandidate("skill-0001")
	candidate.Evidence[0].SignalType = "loaded_skill_text"

	result := gateSkillCandidates([]skillCandidate{candidate}, []skillEvaluation{{
		Ref:    "skill-0001",
		Scores: skillScores(4, 4, 4, 4, 4, 4),
	}})

	assertSingleReject(t, result, "skill-0001", rejectSchemaMissingField)
}

func TestSkillSessionSkillContextRequiresUsedSkillRefs(t *testing.T) {
	candidate := validSkillCandidate("skill-0001")
	candidate.SessionSkillContext = &sessionSkillContext{
		ChangeAgainstLoadedSkill: "Add the verification step learned in this session.",
	}

	result := gateSkillCandidates([]skillCandidate{candidate}, []skillEvaluation{{
		Ref:    "skill-0001",
		Scores: skillScores(4, 4, 4, 4, 4, 4),
	}})

	assertSingleReject(t, result, "skill-0001", rejectSchemaMissingField)
}

func TestSkillSessionSkillContextRequiresChange(t *testing.T) {
	candidate := validSkillCandidate("skill-0001")
	candidate.SessionSkillContext = &sessionSkillContext{
		UsedSkillRefs: []string{"stella-wsl-dev"},
	}

	result := gateSkillCandidates([]skillCandidate{candidate}, []skillEvaluation{{
		Ref:    "skill-0001",
		Scores: skillScores(4, 4, 4, 4, 4, 4),
	}})

	assertSingleReject(t, result, "skill-0001", rejectSchemaMissingField)
}

func TestSkillProcedureActionabilityFloor(t *testing.T) {
	result := gateSkillCandidates([]skillCandidate{
		validSkillCandidate("skill-0001"),
	}, []skillEvaluation{{
		Ref:    "skill-0001",
		Scores: skillScores(4, 4, 4, 1, 4, 4),
	}})

	assertSingleReject(t, result, "skill-0001", rejectScoreFloorFailed)
}

func TestSkillApplicabilitySoftFloor(t *testing.T) {
	result := gateSkillCandidates([]skillCandidate{
		validSkillCandidate("skill-0001"),
	}, []skillEvaluation{{
		Ref:    "skill-0001",
		Scores: skillScores(4, 4, 4, 4, 0, 4),
	}})

	assertSingleReject(t, result, "skill-0001", rejectScoreFloorFailed)
}

func TestSkillVerificationSoftFloor(t *testing.T) {
	result := gateSkillCandidates([]skillCandidate{
		validSkillCandidate("skill-0001"),
	}, []skillEvaluation{{
		Ref:    "skill-0001",
		Scores: skillScores(4, 4, 4, 4, 4, 0),
	}})

	assertSingleReject(t, result, "skill-0001", rejectScoreFloorFailed)
}

func TestSkillCapOrdersByOverallEvidenceThenRef(t *testing.T) {
	candidates := []skillCandidate{
		validSkillCandidate("skill-0002"),
		validSkillCandidate("skill-0001"),
		validSkillCandidate("skill-0003"),
	}
	evaluations := []skillEvaluation{
		{Ref: "skill-0002", Scores: skillScores(4, 2, 4, 3, 4, 4)},
		{Ref: "skill-0001", Scores: skillScores(4, 2, 4, 3, 4, 4)},
		{Ref: "skill-0003", Scores: skillScores(2, 3, 4, 4, 4, 4)},
	}

	result := gateSkillCandidates(candidates, evaluations)

	if !equalRefs(gotRefs(result.Accepted), []CandidateRef{"skill-0001", "skill-0002"}) {
		t.Fatalf("unexpected accepted order: %#v", gotRefs(result.Accepted))
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Ref != "skill-0003" || result.Rejected[0].Reason != rejectCapDropped {
		t.Fatalf("expected skill-0003 cap drop, got %#v", result.Rejected)
	}
}

func TestSkillPromptsDocumentFreshOnlyLoadedSkillBaselineAndNoWriteDecisions(t *testing.T) {
	generationChecks := []string{
		"Read the full bounded review context",
		"fresh conversation",
		"loaded skill text must never be evidence",
		"submit_skill_candidate",
		"finish_skill_generation",
		"Do not output scores",
		"Do not output create",
		"Do not output patch",
	}
	for _, want := range generationChecks {
		if !strings.Contains(skillCandidateGenerationPrompt, want) {
			t.Fatalf("skill generation prompt missing %q", want)
		}
	}

	evaluationChecks := []string{
		"candidate_ref",
		"Do not rewrite candidates",
		"Do not output overall",
		"Do not output passes_threshold",
		"Do not decide create",
		"Do not decide patch",
	}
	for _, want := range evaluationChecks {
		if !strings.Contains(skillCandidateEvaluationPrompt, want) {
			t.Fatalf("skill evaluation prompt missing %q", want)
		}
	}
}

func validSkillCandidate(ref CandidateRef) skillCandidate {
	return skillCandidate{
		Ref: ref,
		Learning: skillLearning{
			Summary:       "Use a bounded review window before reflecting conversation learnings.",
			ReusableDelta: "Build a review unit from fresh messages, then run generation and evaluation separately.",
		},
		Evidence: []skillEvidence{{
			SignalType: skillSignalSuccessfulWorkflow,
			Source:     "fresh conversation showed the bounded review flow working",
			Reason:     "The session produced a reusable implementation sequence.",
		}},
		Applicability: skillApplicability{
			TriggerExamples:    []string{"Implement a reflect candidate pipeline"},
			NonTriggerExamples: []string{"Answer a one-off question about a single error"},
		},
		Procedure: skillProcedure{
			Prerequisites:  []string{"A review target exists"},
			Steps:          []string{"Build the review unit", "Generate candidates", "Evaluate candidates"},
			DecisionPoints: []string{"Skip writing when no accepted candidates exist"},
			Pitfalls:       []string{"Do not advance a watermark past unseen messages"},
			Verification:   []string{"Run the reflect package tests"},
		},
		HandoffHints: skillHandoffHints{SearchQueryHint: "reflect candidate generation evaluation"},
	}
}

func skillScores(evidence, reusable, baseline, procedure, applicability, verification int) map[string]int {
	return map[string]int{
		skillScoreEvidenceStrength:       evidence,
		skillScoreReusableValue:          reusable,
		skillScoreBaselineSeparation:     baseline,
		skillScoreProcedureActionability: procedure,
		skillScoreApplicabilityClarity:   applicability,
		skillScoreVerificationQuality:    verification,
	}
}
