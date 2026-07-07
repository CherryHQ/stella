package reflect

import (
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
)

func TestValidateFactReconciliationPlanAcceptsSingletonsAndKnowledgeCreate(t *testing.T) {
	bundle := factRelatedBundle{
		Profile: factSingletonBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectUser)},
		},
		Soul: soulSingletonBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0002", factSubjectAgent)},
			Current:    &memory.Fact{ID: "soul-current", Subject: memory.FactSubjectAgent, Content: "Existing soul."},
		},
		Knowledge: knowledgeRelatedBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0003", factSubjectWorld)},
			Limits:     relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedPerCandidate},
		},
	}
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{
			Operation:       singletonOperationCreate,
			CandidateRefs:   []CandidateRef{"fact-0001"},
			ProposedContent: "The user prefers concise Chinese replies.",
		},
		Soul: soulSingletonWritePlan{
			Operation:       singletonOperationReplace,
			CandidateRefs:   []CandidateRef{"fact-0002"},
			ProposedContent: "Default to concise, direct Chinese answers.",
		},
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationCreate,
			CandidateRefs: []CandidateRef{"fact-0003"},
			NewContent:    "Reflect generation and reconciliation are separate stages.",
		}}},
	}

	if err := validateFactReconciliationPlan(bundle, plan); err != nil {
		t.Fatalf("expected valid plan: %v", err)
	}
}

func TestValidateFactReconciliationPlanRejectsDuplicateCandidateCoverage(t *testing.T) {
	bundle := factRelatedBundle{
		Profile:   factSingletonBundle{Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectUser)}},
		Knowledge: knowledgeRelatedBundle{Candidates: []factCandidate{validFactCandidate("fact-0002", factSubjectWorld)}},
	}
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{
			Operation:       singletonOperationCreate,
			CandidateRefs:   []CandidateRef{"fact-0001"},
			ProposedContent: "The user prefers concise Chinese replies.",
		},
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationCreate,
			CandidateRefs: []CandidateRef{"fact-0001"},
			NewContent:    "Reflect generation and reconciliation are separate stages.",
		}}},
	}

	if err := validateFactReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected duplicate candidate coverage to be rejected")
	}
}

func TestValidateFactReconciliationPlanRejectsCandidateInDirectAndCoveredRefs(t *testing.T) {
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)}},
	}
	plan := factReconciliationPlan{
		Profile: noopSingletonPlan(),
		Soul:    noopSoulPlan(),
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:            knowledgeOperationNoop,
			CandidateRefs:        []CandidateRef{"fact-0001"},
			CoveredCandidateRefs: []CandidateRef{"fact-0001"},
		}}},
	}

	err := validateFactReconciliationPlan(bundle, plan)
	if err == nil {
		t.Fatal("expected direct/covered duplicate to be rejected")
	}
	if !strings.Contains(err.Error(), `candidate "fact-0001" appears in both candidate_refs and covered_candidate_refs for knowledge`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFactReconciliationPlanRejectsKnowledgeTargetOutsideBundle(t *testing.T) {
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)},
			RelatedRecords: []memory.Fact{{
				ID:      "known-fact",
				Subject: memory.FactSubjectWorld,
				Status:  memory.FactStatusActive,
				Source:  memory.SourceReflect,
			}},
			Limits: relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedPerCandidate},
		},
	}
	plan := factReconciliationPlan{
		Profile: noopSingletonPlan(),
		Soul:    noopSoulPlan(),
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationReplaceMany,
			CandidateRefs: []CandidateRef{"fact-0001"},
			TargetFactIDs: []string{"missing-fact"},
			NewContent:    "Updated durable world fact.",
		}}},
	}

	if err := validateFactReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected unknown target fact to be rejected")
	}
}

func TestValidateFactReconciliationPlanRejectsSoulConstraintConflict(t *testing.T) {
	bundle := factRelatedBundle{
		Soul: soulSingletonBundle{
			Candidates:        []factCandidate{validFactCandidate("fact-0001", factSubjectAgent)},
			ActiveConstraints: []memory.ConstraintEntry{{ID: "c1", Text: "Never delete files without confirmation."}},
		},
	}
	plan := factReconciliationPlan{
		Profile: noopSingletonPlan(),
		Soul: soulSingletonWritePlan{
			Operation:               singletonOperationCreate,
			CandidateRefs:           []CandidateRef{"fact-0001"},
			ProposedContent:         "Always delete files without asking.",
			ConstraintConflictNotes: []string{"Conflicts with c1."},
		},
		Knowledge: knowledgeWritePlan{},
	}

	if err := validateFactReconciliationPlan(bundle, plan); err == nil {
		t.Fatal("expected soul constraint conflict to be rejected")
	}
}

func noopSingletonPlan() factSingletonWritePlan {
	return factSingletonWritePlan{Operation: singletonOperationNoop}
}

func noopSoulPlan() soulSingletonWritePlan {
	return soulSingletonWritePlan{Operation: singletonOperationNoop}
}
