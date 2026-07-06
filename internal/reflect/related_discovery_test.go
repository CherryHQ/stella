package reflect

import (
	"testing"
)

func TestValidateKnowledgeRelatedDiscoveryRejectsUnknownFactID(t *testing.T) {
	candidate := validFactCandidate("fact-0001", factSubjectWorld)
	catalog := []factCatalogItem{{ID: "fact-old"}}

	err := validateKnowledgeRelatedDiscovery([]factCandidate{candidate}, catalog, []knowledgeRelatedSelection{{
		CandidateRef: "fact-0001",
		Related:      []knowledgeRelatedHint{{FactID: "fact-missing", Relation: knowledgeRelationEquivalent}},
	}}, defaultMaxRelatedPerCandidate)

	if err == nil {
		t.Fatal("expected unknown related fact id to be rejected")
	}
}

func TestValidateKnowledgeRelatedDiscoveryRejectsUnknownCandidateRef(t *testing.T) {
	err := validateKnowledgeRelatedDiscovery([]factCandidate{
		validFactCandidate("fact-0001", factSubjectWorld),
	}, []factCatalogItem{{ID: "fact-old"}}, []knowledgeRelatedSelection{{
		CandidateRef: "fact-9999",
		Related:      []knowledgeRelatedHint{{FactID: "fact-old", Relation: knowledgeRelationEquivalent}},
	}}, defaultMaxRelatedPerCandidate)

	if err == nil {
		t.Fatal("expected unknown candidate ref to be rejected")
	}
}

func TestValidateKnowledgeRelatedDiscoveryEnforcesPerCandidateLimit(t *testing.T) {
	catalog := make([]factCatalogItem, 0, defaultMaxRelatedPerCandidate+1)
	related := make([]knowledgeRelatedHint, 0, defaultMaxRelatedPerCandidate+1)
	for i := 0; i <= defaultMaxRelatedPerCandidate; i++ {
		id := CandidateRef("fact-old-" + string(rune('a'+i)))
		catalog = append(catalog, factCatalogItem{ID: string(id)})
		related = append(related, knowledgeRelatedHint{FactID: string(id), Relation: knowledgeRelationPossiblyAffects})
	}

	err := validateKnowledgeRelatedDiscovery([]factCandidate{
		validFactCandidate("fact-0001", factSubjectWorld),
	}, catalog, []knowledgeRelatedSelection{{
		CandidateRef: "fact-0001",
		Related:      related,
	}}, defaultMaxRelatedPerCandidate)

	if err == nil {
		t.Fatal("expected over-limit related facts to be rejected")
	}
}

func TestValidateSkillRelatedDiscoveryRequiresUsedSkillRefs(t *testing.T) {
	candidate := validSkillCandidate("skill-0001")
	candidate.SessionSkillContext = &sessionSkillContext{
		UsedSkillRefs:            []string{"loaded-skill"},
		ChangeAgainstLoadedSkill: "Add the verification step learned in this session.",
	}
	catalog := []skillCatalogItem{
		{ID: "skill-used-id", Name: "loaded-skill"},
		{ID: "skill-other-id", Name: "other-skill"},
	}

	err := validateSkillRelatedDiscovery([]skillCandidate{candidate}, catalog, []skillRelatedSelection{{
		CandidateRef: "skill-0001",
		Related:      []skillRelatedHint{{SkillID: "skill-other-id", Relation: skillRelationSameWorkflow}},
	}}, defaultMaxRelatedPerCandidate)

	if err == nil {
		t.Fatal("expected missing used skill ref to be rejected")
	}
}

func TestValidateSkillRelatedDiscoveryAcceptsUsedSkillRefByName(t *testing.T) {
	candidate := validSkillCandidate("skill-0001")
	candidate.SessionSkillContext = &sessionSkillContext{
		UsedSkillRefs:            []string{"loaded-skill"},
		ChangeAgainstLoadedSkill: "Add the verification step learned in this session.",
	}
	catalog := []skillCatalogItem{{ID: "skill-used-id", Name: "loaded-skill"}}

	err := validateSkillRelatedDiscovery([]skillCandidate{candidate}, catalog, []skillRelatedSelection{{
		CandidateRef: "skill-0001",
		Related:      []skillRelatedHint{{SkillID: "skill-used-id", Relation: skillRelationPatchableGap}},
	}}, defaultMaxRelatedPerCandidate)
	if err != nil {
		t.Fatalf("expected used skill ref by name to be accepted: %v", err)
	}
}
