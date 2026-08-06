package reflect

import (
	"strings"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestValidateSkillReconciliationPlanAcceptsCreateAndPatch(t *testing.T) {
	first := validSkillCandidate("skill-0001")
	second := validSkillCandidate("skill-0002")
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{first, second},
		RelatedRecords: []skillRelatedRecord{{
			ContentDigest: strings.Repeat("a", 64),
			Skill: pkgplugins.Skill{
				ID:            "old-skill",
				Scope:         "user_agent",
				Status:        "active",
				ContentDigest: strings.Repeat("a", 64),
				Version:       3,
				Metadata:      []byte(`{"created_by":"reflect"}`),
			},
			MainFileContent: "# Old skill\n",
		}},
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{"skill-0001"},
			Name:            "new-reflect-skill",
			Description:     "A reusable reflect-maintained workflow.",
			MainFileContent: "# New reflect skill\n",
		},
		{
			Operation:             skillOperationPatch,
			CandidateRefs:         []CandidateRef{"skill-0002"},
			TargetSkillID:         "old-skill",
			ExpectedContentDigest: strings.Repeat("a", 64),
			Description:           "Updated reusable reflect workflow.",
			MainFileContent:       "# Updated skill\n",
		},
	}}

	if err := validateSkillReconciliationPlan(bundle, plan); err != nil {
		t.Fatalf("expected valid skill plan: %v", err)
	}
}

func TestValidateSkillReconciliationPlanRejectsPatchTargetOutsideBundle(t *testing.T) {
	bundle := skillRelatedBundle{Candidates: []skillCandidate{validSkillCandidate("skill-0001")}}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:            skillOperationPatch,
		CandidateRefs:        []CandidateRef{"skill-0001"},
		TargetSkillID:        "missing-skill",
		ExpectedSkillVersion: 1,
		MainFileContent:      "# Updated skill\n",
	}}}

	if err := validateSkillReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected missing patch target to be rejected")
	}
}

func TestValidateSkillReconciliationPlanRejectsDigestMismatch(t *testing.T) {
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{validSkillCandidate("skill-0001")},
		RelatedRecords: []skillRelatedRecord{{
			ContentDigest: strings.Repeat("a", 64),
			Skill:         pkgplugins.Skill{ID: "old-skill", Scope: "user_agent", Status: "active", ContentDigest: strings.Repeat("a", 64), Version: 3, Metadata: []byte(`{"created_by":"reflect"}`)},
		}},
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:             skillOperationPatch,
		CandidateRefs:         []CandidateRef{"skill-0001"},
		TargetSkillID:         "old-skill",
		ExpectedContentDigest: strings.Repeat("b", 64),
		MainFileContent:       "# Updated skill\n",
	}}}

	if err := validateSkillReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected stale skill version to be rejected")
	}
}

func TestValidateSkillReconciliationPlanRejectsDuplicateCandidateCoverage(t *testing.T) {
	bundle := skillRelatedBundle{Candidates: []skillCandidate{validSkillCandidate("skill-0001")}}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{"skill-0001"},
			Name:            "first-skill",
			Description:     "First skill.",
			MainFileContent: "# First skill\n",
		},
		{
			Operation:       skillOperationCreate,
			CandidateRefs:   []CandidateRef{"skill-0001"},
			Name:            "second-skill",
			Description:     "Second skill.",
			MainFileContent: "# Second skill\n",
		},
	}}

	if err := validateSkillReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected duplicate candidate coverage to be rejected")
	}
}
