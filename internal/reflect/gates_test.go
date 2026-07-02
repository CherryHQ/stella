package reflect

import "testing"

func TestCandidateRefStableAssignment(t *testing.T) {
	refs := assignCandidateRefs("fact", 3)
	want := []CandidateRef{"fact-0001", "fact-0002", "fact-0003"}
	if len(refs) != len(want) {
		t.Fatalf("expected %d refs, got %d", len(want), len(refs))
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("ref %d = %q, want %q", i, refs[i], want[i])
		}
	}
	if got := candidateRef("skill", 11); got != "skill-0012" {
		t.Fatalf("unexpected indexed ref: %q", got)
	}
}

func TestGateRejectsCoreFloorFailure(t *testing.T) {
	result := gateCandidates([]CandidateGateInput{{
		Ref:     "fact-0001",
		Content: "The user explicitly prefers concise Chinese answers.",
		Scores: map[string]int{
			"evidence_strength": 1,
			"subject_fit":       4,
			"durability":        4,
			"future_utility":    4,
			"atomicity":         4,
		},
	}}, factGateConfigForTest(1))

	if len(result.Accepted) != 0 {
		t.Fatalf("expected no accepted candidates, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != rejectScoreFloorFailed {
		t.Fatalf("expected floor rejection, got %#v", result.Rejected)
	}
}

func TestGateRejectsOverallBelowThreshold(t *testing.T) {
	cfg := factGateConfigForTest(1)
	cfg.Threshold = 0.7
	result := gateCandidates([]CandidateGateInput{{
		Ref:     "fact-0001",
		Content: "The user explicitly prefers concise Chinese answers.",
		Scores: map[string]int{
			"evidence_strength": 2,
			"subject_fit":       2,
			"durability":        2,
			"future_utility":    2,
			"atomicity":         2,
		},
	}}, cfg)

	if len(result.Accepted) != 0 {
		t.Fatalf("expected no accepted candidates, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != rejectOverallBelowThreshold {
		t.Fatalf("expected threshold rejection, got %#v", result.Rejected)
	}
}

func TestGateRejectsSecretLikeContent(t *testing.T) {
	result := gateCandidates([]CandidateGateInput{{
		Ref:     "fact-0001",
		Content: "The user's GitHub token is ghp_abcdefghijklmnopqrstuvwxyz1234567890.",
		Scores: map[string]int{
			"evidence_strength": 4,
			"subject_fit":       4,
			"durability":        4,
			"future_utility":    4,
			"atomicity":         4,
		},
	}}, factGateConfigForTest(1))

	if len(result.Accepted) != 0 {
		t.Fatalf("expected no accepted candidates, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != rejectSecretDetected {
		t.Fatalf("expected secret rejection, got %#v", result.Rejected)
	}
}

func TestCapOrdersFactsByOverallSubjectFitThenRef(t *testing.T) {
	result := gateCandidates([]CandidateGateInput{
		{
			Ref:     "fact-0002",
			Content: "Candidate two.",
			Scores: map[string]int{
				"evidence_strength": 2,
				"subject_fit":       4,
				"durability":        4,
				"future_utility":    4,
				"atomicity":         4,
			},
		},
		{
			Ref:     "fact-0001",
			Content: "Candidate one.",
			Scores: map[string]int{
				"evidence_strength": 2,
				"subject_fit":       4,
				"durability":        4,
				"future_utility":    4,
				"atomicity":         4,
			},
		},
		{
			Ref:     "fact-0003",
			Content: "Candidate three.",
			Scores: map[string]int{
				"evidence_strength": 3,
				"subject_fit":       3,
				"durability":        4,
				"future_utility":    4,
				"atomicity":         4,
			},
		},
	}, factGateConfigForTest(2))

	if !equalRefs(gotRefs(result.Accepted), []CandidateRef{"fact-0001", "fact-0002"}) {
		t.Fatalf("unexpected accepted order: %#v", gotRefs(result.Accepted))
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Ref != "fact-0003" || result.Rejected[0].Reason != rejectCapDropped {
		t.Fatalf("expected fact-0003 cap drop, got %#v", result.Rejected)
	}
}

func TestCapOrdersSkillsByOverallEvidenceThenRef(t *testing.T) {
	result := gateCandidates([]CandidateGateInput{
		{
			Ref:     "skill-0002",
			Content: "Candidate two.",
			Scores: map[string]int{
				"evidence_strength":    4,
				"reusable_value":       2,
				"baseline_separation":  4,
				"trigger_boundary":     4,
				"procedure_quality":    4,
				"verification_quality": 4,
			},
		},
		{
			Ref:     "skill-0001",
			Content: "Candidate one.",
			Scores: map[string]int{
				"evidence_strength":    4,
				"reusable_value":       2,
				"baseline_separation":  4,
				"trigger_boundary":     4,
				"procedure_quality":    4,
				"verification_quality": 4,
			},
		},
		{
			Ref:     "skill-0003",
			Content: "Candidate three.",
			Scores: map[string]int{
				"evidence_strength":    3,
				"reusable_value":       3,
				"baseline_separation":  4,
				"trigger_boundary":     4,
				"procedure_quality":    4,
				"verification_quality": 4,
			},
		},
	}, skillGateConfigForTest(2))

	if !equalRefs(gotRefs(result.Accepted), []CandidateRef{"skill-0001", "skill-0002"}) {
		t.Fatalf("unexpected accepted order: %#v", gotRefs(result.Accepted))
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Ref != "skill-0003" || result.Rejected[0].Reason != rejectCapDropped {
		t.Fatalf("expected skill-0003 cap drop, got %#v", result.Rejected)
	}
}

func factGateConfigForTest(cap int) CandidateGateConfig {
	return CandidateGateConfig{
		Weights: map[string]float64{
			"evidence_strength": 1,
			"subject_fit":       1,
			"durability":        1,
			"future_utility":    1,
			"atomicity":         1,
		},
		CoreFields:     []string{"evidence_strength", "subject_fit", "durability", "future_utility", "atomicity"},
		CoreFloor:      2,
		Threshold:      0.5,
		Cap:            cap,
		TieBreakFields: []string{"subject_fit"},
	}
}

func skillGateConfigForTest(cap int) CandidateGateConfig {
	return CandidateGateConfig{
		Weights: map[string]float64{
			"evidence_strength":    1,
			"reusable_value":       1,
			"baseline_separation":  1,
			"trigger_boundary":     1,
			"procedure_quality":    1,
			"verification_quality": 1,
		},
		CoreFields:     []string{"evidence_strength", "reusable_value", "baseline_separation"},
		CoreFloor:      2,
		Threshold:      0.5,
		Cap:            cap,
		TieBreakFields: []string{"evidence_strength"},
	}
}

func gotRefs(decisions []CandidateGateDecision) []CandidateRef {
	refs := make([]CandidateRef, 0, len(decisions))
	for _, decision := range decisions {
		refs = append(refs, decision.Ref)
	}
	return refs
}

func equalRefs(a, b []CandidateRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
