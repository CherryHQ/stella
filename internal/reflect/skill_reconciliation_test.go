package reflect

import (
	"testing"

	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/ai"
)

const testSkillContentDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestValidateSkillReconciliationPlanAcceptsCreateAndPatch(t *testing.T) {
	first := validSkillCandidate("skill-0001")
	second := validSkillCandidate("skill-0002")
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{first, second},
		RelatedRecords: []skillRelatedRecord{{
			Skill: skills.Skill{
				ID:            "old-skill",
				Scope:         "user_agent",
				Status:        "active",
				Version:       3,
				ContentDigest: testSkillContentDigest,
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
			Operation:           skillOperationPatch,
			CandidateRefs:       []CandidateRef{"skill-0002"},
			TargetSkillID:       "old-skill",
			ExpectedSkillDigest: testSkillContentDigest,
			Description:         "Updated reusable reflect workflow.",
			MainFileContent:     "# Updated skill\n",
		},
	}}

	if err := validateSkillReconciliationPlan(bundle, plan); err != nil {
		t.Fatalf("expected valid skill plan: %v", err)
	}
}

func TestSkillReconciliationSchemaCarriesValidatedPatchDigest(t *testing.T) {
	properties, ok := skillWriteOperationSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatal("skill operation schema has no properties")
	}
	if _, ok := properties["expected_skill_digest"]; !ok {
		t.Fatal("skill operation schema omits validated expected_skill_digest")
	}
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{validSkillCandidate("skill-0001")},
		RelatedRecords: []skillRelatedRecord{{
			Skill: skills.Skill{
				ID: "old-skill", Scope: "user_agent", Status: "active",
				ContentDigest: testSkillContentDigest, Metadata: []byte(`{"created_by":"reflect"}`),
			},
			MainFileContent: "# Old skill\n",
		}},
	}
	plan, err := decodeSkillReconciliationCall([]ai.ToolCall{rawToolCall(toolSubmitSkillReconciliation, `{"plan":{"operations":[{
		"operation":"patch_skill",
		"candidate_refs":["skill-0001"],
		"target_skill_id":"old-skill",
		"expected_skill_digest":"`+testSkillContentDigest+`",
		"main_file_content":"# Updated skill\n"
	}]}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSkillReconciliationPlan(bundle, plan); err != nil {
		t.Fatalf("schema-expressible patch rejected: %v", err)
	}
}

func TestValidateSkillReconciliationPlanRejectsPatchTargetOutsideBundle(t *testing.T) {
	bundle := skillRelatedBundle{Candidates: []skillCandidate{validSkillCandidate("skill-0001")}}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:       skillOperationPatch,
		CandidateRefs:   []CandidateRef{"skill-0001"},
		TargetSkillID:   "missing-skill",
		MainFileContent: "# Updated skill\n",
	}}}

	if err := validateSkillReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected missing patch target to be rejected")
	}
}

func TestValidateSkillReconciliationPlanRejectsDigestMismatch(t *testing.T) {
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{validSkillCandidate("skill-0001")},
		RelatedRecords: []skillRelatedRecord{{
			Skill: skills.Skill{ID: "old-skill", Scope: "user_agent", Status: "active", Version: 3, ContentDigest: testSkillContentDigest, Metadata: []byte(`{"created_by":"reflect"}`)},
		}},
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{{
		Operation:           skillOperationPatch,
		CandidateRefs:       []CandidateRef{"skill-0001"},
		TargetSkillID:       "old-skill",
		ExpectedSkillDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainFileContent:     "# Updated skill\n",
	}}}

	if err := validateSkillReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected stale skill digest to be rejected")
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
