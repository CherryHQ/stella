package reflect

import (
	"fmt"
	"testing"
)

func TestValidateKnowledgeRelatedDiscoveryRejectsUnknownFactID(t *testing.T) {
	candidate := validFactCandidate("fact-0001", factSubjectWorld)
	catalog := []factCatalogItem{{ID: "fact-old"}}

	err := validateKnowledgeRelatedDiscovery([]factCandidate{candidate}, catalog, []knowledgeRelatedSelection{{
		CandidateRef: "fact-0001",
		Related:      []knowledgeRelatedHint{{FactID: "fact-missing", Relation: knowledgeRelationEquivalent}},
	}}, defaultMaxRelatedKnowledgePerCandidate)

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
	}}, defaultMaxRelatedKnowledgePerCandidate)

	if err == nil {
		t.Fatal("expected unknown candidate ref to be rejected")
	}
}

func TestValidateKnowledgeRelatedDiscoveryEnforcesPerCandidateLimit(t *testing.T) {
	catalog := make([]factCatalogItem, 0, defaultMaxRelatedKnowledgePerCandidate+1)
	related := make([]knowledgeRelatedHint, 0, defaultMaxRelatedKnowledgePerCandidate+1)
	for i := 0; i <= defaultMaxRelatedKnowledgePerCandidate; i++ {
		id := CandidateRef("fact-old-" + string(rune('a'+i)))
		catalog = append(catalog, factCatalogItem{ID: string(id)})
		related = append(related, knowledgeRelatedHint{FactID: string(id), Relation: knowledgeRelationPossiblyAffects})
	}

	err := validateKnowledgeRelatedDiscovery([]factCandidate{
		validFactCandidate("fact-0001", factSubjectWorld),
	}, catalog, []knowledgeRelatedSelection{{
		CandidateRef: "fact-0001",
		Related:      related,
	}}, defaultMaxRelatedKnowledgePerCandidate)

	if err == nil {
		t.Fatal("expected over-limit related facts to be rejected")
	}
}

func TestValidateKnowledgeRelatedDiscoveryAllowsTen(t *testing.T) {
	catalog := make([]factCatalogItem, 0, 10)
	related := make([]knowledgeRelatedHint, 0, 10)
	for i := range 10 {
		id := fmt.Sprintf("fact-old-%d", i)
		catalog = append(catalog, factCatalogItem{ID: id})
		related = append(related, knowledgeRelatedHint{FactID: id, Relation: knowledgeRelationPossiblyAffects})
	}

	err := validateKnowledgeRelatedDiscovery([]factCandidate{
		validFactCandidate("fact-0001", factSubjectWorld),
	}, catalog, []knowledgeRelatedSelection{{
		CandidateRef: "fact-0001",
		Related:      related,
	}}, 0)
	if err != nil {
		t.Fatalf("expected ten related knowledge records to be accepted: %v", err)
	}
}

func TestNormalizeKnowledgeRelatedDiscoveryAggregatesCandidateAndKeepsStrongestRelation(t *testing.T) {
	got := normalizeKnowledgeRelatedSelections([]knowledgeRelatedSelection{
		{
			CandidateRef: "fact-0001",
			Related: []knowledgeRelatedHint{
				{FactID: "fact-old", Relation: knowledgeRelationPossiblyAffects},
			},
		},
		{
			CandidateRef: "fact-0001",
			Related: []knowledgeRelatedHint{
				{FactID: "fact-old", Relation: knowledgeRelationConflict},
				{FactID: "fact-other", Relation: knowledgeRelationSameEntitySlot},
			},
		},
	})

	if len(got) != 1 || len(got[0].Related) != 2 {
		t.Fatalf("expected duplicate fact id to be deduped, got %#v", got)
	}
	if got[0].Related[0].FactID != "fact-old" || got[0].Related[0].Relation != knowledgeRelationConflict {
		t.Fatalf("expected conflict to win for duplicate fact, got %#v", got[0].Related[0])
	}
	if got[0].Related[1].FactID != "fact-other" {
		t.Fatalf("expected first unique order to be preserved, got %#v", got[0].Related)
	}
}

func TestNormalizeKnowledgeRelatedDiscoveryExposesAggregateLimitViolation(t *testing.T) {
	catalog := make([]factCatalogItem, 0, defaultMaxRelatedKnowledgePerCandidate+1)
	selections := make([]knowledgeRelatedSelection, 0, 2)
	for batch := range 2 {
		related := make([]knowledgeRelatedHint, 0, defaultMaxRelatedKnowledgePerCandidate)
		for i := batch; i <= defaultMaxRelatedKnowledgePerCandidate; i += 2 {
			id := fmt.Sprintf("fact-old-%d", i)
			catalog = append(catalog, factCatalogItem{ID: id})
			related = append(related, knowledgeRelatedHint{FactID: id, Relation: knowledgeRelationPossiblyAffects})
		}
		selections = append(selections, knowledgeRelatedSelection{CandidateRef: "fact-0001", Related: related})
	}

	normalized := normalizeKnowledgeRelatedSelections(selections)
	err := validateKnowledgeRelatedDiscovery(
		[]factCandidate{validFactCandidate("fact-0001", factSubjectWorld)},
		catalog,
		normalized,
		defaultMaxRelatedKnowledgePerCandidate,
	)
	if err == nil {
		t.Fatal("expected aggregate related facts over the per-candidate limit to be rejected")
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
	}}, defaultMaxRelatedSkillsPerCandidate)

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
	}}, defaultMaxRelatedSkillsPerCandidate)
	if err != nil {
		t.Fatalf("expected used skill ref by name to be accepted: %v", err)
	}
}

func TestValidateSkillRelatedDiscoveryAllowsFiveAndRejectsSix(t *testing.T) {
	catalog := make([]skillCatalogItem, 0, 6)
	related := make([]skillRelatedHint, 0, 6)
	for i := range 6 {
		id := fmt.Sprintf("skill-old-%d", i)
		catalog = append(catalog, skillCatalogItem{ID: id})
		related = append(related, skillRelatedHint{SkillID: id, Relation: skillRelationSameWorkflow})
	}
	candidates := []skillCandidate{validSkillCandidate("skill-0001")}

	if err := validateSkillRelatedDiscovery(candidates, catalog, []skillRelatedSelection{{
		CandidateRef: "skill-0001",
		Related:      related[:5],
	}}, 0); err != nil {
		t.Fatalf("expected five related skills to be accepted: %v", err)
	}
	if err := validateSkillRelatedDiscovery(candidates, catalog, []skillRelatedSelection{{
		CandidateRef: "skill-0001",
		Related:      related,
	}}, 0); err == nil {
		t.Fatal("expected six related skills to be rejected")
	}
}

func TestNormalizeSkillRelatedDiscoveryAggregatesCandidateAndKeepsStrongestRelation(t *testing.T) {
	got := normalizeSkillRelatedSelections([]skillRelatedSelection{
		{
			CandidateRef: "skill-0001",
			Related: []skillRelatedHint{
				{SkillID: "skill-old", Relation: skillRelationOverlappingTrigger},
			},
		},
		{
			CandidateRef: "skill-0001",
			Related: []skillRelatedHint{
				{SkillID: "skill-old", Relation: skillRelationPatchableGap},
				{SkillID: "skill-other", Relation: skillRelationSameWorkflow},
			},
		},
	})

	if len(got) != 1 || len(got[0].Related) != 2 {
		t.Fatalf("expected duplicate skill id to be deduped, got %#v", got)
	}
	if got[0].Related[0].SkillID != "skill-old" || got[0].Related[0].Relation != skillRelationPatchableGap {
		t.Fatalf("expected patchable_gap to win for duplicate skill, got %#v", got[0].Related[0])
	}
	if got[0].Related[1].SkillID != "skill-other" {
		t.Fatalf("expected first unique order to be preserved, got %#v", got[0].Related)
	}
}

func TestNormalizeSkillRelatedDiscoveryExposesAggregateLimitViolation(t *testing.T) {
	catalog := make([]skillCatalogItem, 0, defaultMaxRelatedSkillsPerCandidate+1)
	selections := make([]skillRelatedSelection, 0, 2)
	for batch := range 2 {
		related := make([]skillRelatedHint, 0, defaultMaxRelatedSkillsPerCandidate)
		for i := batch; i <= defaultMaxRelatedSkillsPerCandidate; i += 2 {
			id := fmt.Sprintf("skill-old-%d", i)
			catalog = append(catalog, skillCatalogItem{ID: id})
			related = append(related, skillRelatedHint{SkillID: id, Relation: skillRelationOverlappingTrigger})
		}
		selections = append(selections, skillRelatedSelection{CandidateRef: "skill-0001", Related: related})
	}

	normalized := normalizeSkillRelatedSelections(selections)
	err := validateSkillRelatedDiscovery(
		[]skillCandidate{validSkillCandidate("skill-0001")},
		catalog,
		normalized,
		defaultMaxRelatedSkillsPerCandidate,
	)
	if err == nil {
		t.Fatal("expected aggregate related skills over the per-candidate limit to be rejected")
	}
}
