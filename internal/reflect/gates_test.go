package reflect

import (
	"strings"
	"testing"
)

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

func TestGateRejectsUnknownScoreField(t *testing.T) {
	result := gateCandidates([]CandidateGateInput{{
		Ref:     "fact-0001",
		Content: "The user explicitly prefers concise Chinese answers.",
		Scores: map[string]int{
			"evidence_strength": 4,
			"subject_fit":       4,
			"durability":        4,
			"future_utility":    4,
			"atomicity":         4,
			"confidence":        4,
		},
	}}, factGateConfigForTest(1))

	if len(result.Accepted) != 0 {
		t.Fatalf("expected no accepted candidates, got %#v", result.Accepted)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != rejectSchemaMissingField {
		t.Fatalf("expected schema rejection, got %#v", result.Rejected)
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

func TestSanitizeSecretLikeContent(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secret string
	}{
		{
			name:   "URI userinfo",
			input:  "connect to postgres://app:correct-horse-battery-staple@db.internal/app",
			secret: "correct-horse-battery-staple",
		},
		{
			name:   "bearer",
			input:  "Authorization: Bearer fake-access-token-12345",
			secret: "fake-access-token-12345",
		},
		{
			name:   "basic",
			input:  "Authorization: Basic dXNlcjpwYXNz",
			secret: "dXNlcjpwYXNz",
		},
		{
			name:   "JWT",
			input:  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.fakeSignature123",
			secret: "eyJhbGciOiJIUzI1NiJ9",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, detected := sanitizeSecretLikeContent(test.input)
			if !detected {
				t.Fatalf("expected secret detection for %q", test.input)
			}
			if strings.Contains(got, test.secret) {
				t.Fatalf("sanitized content still contains %q: %q", test.secret, got)
			}
			if !strings.Contains(got, reflectSecretRedaction) {
				t.Fatalf("sanitized content is missing redaction marker: %q", got)
			}
		})
	}
}

func TestSanitizeSecretLikeContentDoesNotMatchAuthSchemeProse(t *testing.T) {
	tests := []string{
		"Basic authentication is required",
		"basic validation rules",
		"bearer instrument",
		"Authorization: Basic authentication",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			got, detected := sanitizeSecretLikeContent(input)
			if detected {
				t.Fatalf("unexpected secret detection for %q: %q", input, got)
			}
			if got != input {
				t.Fatalf("sanitizer changed benign input: got %q, want %q", got, input)
			}
		})
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

func TestSkillGateUsesConfiguredThreshold(t *testing.T) {
	midCandidate := validSkillCandidate("skill-0001")
	midEvaluation := skillEvaluation{
		Ref: midCandidate.Ref,
		Scores: map[string]int{
			skillScoreEvidenceStrength:       3,
			skillScoreReusableValue:          3,
			skillScoreBaselineSeparation:     3,
			skillScoreProcedureActionability: 3,
			skillScoreApplicabilityClarity:   3,
			skillScoreVerificationQuality:    3,
		},
	}
	defaultResult := gateSkillCandidates([]skillCandidate{midCandidate}, []skillEvaluation{midEvaluation})
	if len(defaultResult.Rejected) != 1 || defaultResult.Rejected[0].Reason != rejectOverallBelowThreshold {
		t.Fatalf("expected default skill threshold to reject mid-score candidate, got %#v", defaultResult)
	}

	borderlineCandidate := validSkillCandidate("skill-0003")
	borderlineEvaluation := skillEvaluation{
		Ref: borderlineCandidate.Ref,
		Scores: map[string]int{
			skillScoreEvidenceStrength:       4,
			skillScoreReusableValue:          3,
			skillScoreBaselineSeparation:     3,
			skillScoreProcedureActionability: 3,
			skillScoreApplicabilityClarity:   3,
			skillScoreVerificationQuality:    3,
		},
	}
	borderlineResult := gateSkillCandidates([]skillCandidate{borderlineCandidate}, []skillEvaluation{borderlineEvaluation})
	if len(borderlineResult.Accepted) != 1 {
		t.Fatalf("expected default skill threshold to accept 0.80 candidate, got %#v", borderlineResult)
	}

	strongCandidate := validSkillCandidate("skill-0004")
	strongEvaluation := skillEvaluation{
		Ref: strongCandidate.Ref,
		Scores: map[string]int{
			skillScoreEvidenceStrength:       4,
			skillScoreReusableValue:          4,
			skillScoreBaselineSeparation:     4,
			skillScoreProcedureActionability: 4,
			skillScoreApplicabilityClarity:   4,
			skillScoreVerificationQuality:    4,
		},
	}
	strongResult := gateSkillCandidates([]skillCandidate{strongCandidate}, []skillEvaluation{strongEvaluation})
	if len(strongResult.Accepted) != 1 {
		t.Fatalf("expected default skill threshold to accept strong candidate, got %#v", strongResult)
	}

	customResult := gateSkillCandidatesWithSettings([]skillCandidate{midCandidate}, []skillEvaluation{midEvaluation}, CandidateGateSettings{
		SkillThreshold: 0.70,
	})
	if len(customResult.Accepted) != 1 {
		t.Fatalf("expected configured lower skill threshold to accept mid-score candidate, got %#v", customResult)
	}
}

func TestFactGateUsesConfiguredThreshold(t *testing.T) {
	candidate := validFactCandidate("fact-0001", factSubjectWorld)
	evaluation := factEvaluation{
		Ref: candidate.Ref,
		Scores: map[string]int{
			factScoreEvidenceStrength: 3,
			factScoreSubjectFit:       3,
			factScoreDurability:       3,
			factScoreFutureUtility:    3,
			factScoreAtomicity:        3,
		},
	}
	defaultResult := gateFactCandidates([]factCandidate{candidate}, []factEvaluation{evaluation}, factGateOptions{PrivateOneToOne: true})
	if len(defaultResult.Accepted) != 1 {
		t.Fatalf("expected default threshold to accept candidate, got %#v", defaultResult)
	}

	customResult := gateFactCandidatesWithSettings([]factCandidate{candidate}, []factEvaluation{evaluation}, factGateOptions{PrivateOneToOne: true}, CandidateGateSettings{
		FactThreshold: 0.80,
	})
	if len(customResult.Rejected) != 1 || customResult.Rejected[0].Reason != rejectOverallBelowThreshold {
		t.Fatalf("expected configured threshold rejection, got %#v", customResult)
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
