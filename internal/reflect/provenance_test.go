package reflect

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/skill"
)

func TestBuildFactBatchOperationsSelectsOnlyWrittenOperationData(t *testing.T) {
	written := validFactCandidate("fact-0001", factSubjectWorld)
	noop := validFactCandidate("fact-0002", factSubjectWorld)
	input := factProvenanceInput{
		Context: testReflectProvenanceContext(),
		Decisions: []factCandidateDecision{
			testFactCandidateDecision(written, 0.91),
			testFactCandidateDecision(noop, 0.88),
		},
	}
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{
			Candidates: []factCandidate{written, noop},
			RelatedRecords: []memory.Fact{{
				ID:      "fact-old",
				Version: 7,
				Content: "full related fact content must not be copied",
			}},
			RelationHints: []knowledgeRelatedSelection{
				{
					CandidateRef: written.Ref,
					Related: []knowledgeRelatedHint{{
						FactID:   "fact-old",
						Relation: knowledgeRelationConflict,
					}},
					Reason: "same entity slot",
				},
				{CandidateRef: noop.Ref},
			},
		},
	}
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{Operation: singletonOperationNoop},
		Soul:    soulSingletonWritePlan{Operation: singletonOperationNoop},
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{
			{
				Operation:     knowledgeOperationReplaceMany,
				CandidateRefs: []CandidateRef{written.Ref},
				TargetFactIDs: []string{"fact-old"},
				NewContent:    "new fact",
				Rationale:     "fresh correction",
			},
			{
				Operation:     knowledgeOperationNoop,
				CandidateRefs: []CandidateRef{noop.Ref},
				Rationale:     "already represented",
			},
		}},
	}

	got, err := buildFactBatchOperations(bundle, plan, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("operation count = %d, want 1", len(got))
	}
	var decoded reflectProvenanceMetadata[factOperationProvenance]
	if err := json.Unmarshal(got[0].ChangelogMetadata, &decoded); err != nil {
		t.Fatal(err)
	}
	provenance := decoded.ReflectProvenance
	if provenance.OperationRef != "knowledge-0001" || provenance.Line != reflectLineFact {
		t.Fatalf("unexpected provenance header: %#v", provenance.reflectProvenanceHeader)
	}
	if len(provenance.Candidates) != 1 || provenance.Candidates[0].Ref != written.Ref {
		t.Fatalf("unexpected candidates: %#v", provenance.Candidates)
	}
	if len(provenance.Evaluations) != 1 || provenance.Evaluations[0].NormalizedOverall != 0.91 {
		t.Fatalf("unexpected evaluations: %#v", provenance.Evaluations)
	}
	if len(provenance.RelatedRecords) != 1 || provenance.RelatedRecords[0].Version != 7 ||
		provenance.RelatedRecords[0].Relation != knowledgeRelationConflict {
		t.Fatalf("unexpected related projection: %#v", provenance.RelatedRecords)
	}
	if strings.Contains(string(got[0].ChangelogMetadata), "full related fact content") {
		t.Fatal("provenance copied full related fact content")
	}
}

func TestBuildFactBatchOperationsUsesEmptyRelatedRecordArray(t *testing.T) {
	candidate := validFactCandidate("fact-0001", factSubjectUser)
	input := factProvenanceInput{
		Context:   testReflectProvenanceContext(),
		Decisions: []factCandidateDecision{testFactCandidateDecision(candidate, 0.9)},
	}
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{
			Operation:       singletonOperationCreate,
			CandidateRefs:   []CandidateRef{candidate.Ref},
			ProposedContent: "new profile",
			Rationale:       "durable explicit preference",
		},
		Soul: soulSingletonWritePlan{Operation: singletonOperationNoop},
	}

	got, err := buildFactBatchOperations(factRelatedBundle{}, plan, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(string(got[0].ChangelogMetadata), `"related_records":[]`) {
		t.Fatalf("expected stable empty related_records array, got %s", got[0].ChangelogMetadata)
	}
}

func TestBuildSkillPlanProvenanceUsesPlanIndexAndContentDigest(t *testing.T) {
	candidate := validSkillCandidate("skill-0001")
	mainFile := "# Generated skill\n\nDo the reusable workflow.\n"
	input := skillProvenanceInput{
		Context:   testReflectProvenanceContext(),
		Decisions: []skillCandidateDecision{testSkillCandidateDecision(candidate, 0.93)},
	}
	bundle := skillRelatedBundle{
		Candidates: []skillCandidate{candidate},
		RelatedRecords: []skillRelatedRecord{{
			Skill: skill.Skill{
				ID:      "skill-old",
				Version: 4,
			},
			MainFileContent: "full old SKILL.md must not be copied",
		}},
		RelationHints: []skillRelatedSelection{{
			CandidateRef: candidate.Ref,
			Related: []skillRelatedHint{{
				SkillID:  "skill-old",
				Relation: skillRelationPatchableGap,
			}},
			Reason: "same workflow",
		}},
	}
	plan := skillReconciliationPlan{Operations: []skillWriteOperation{
		{
			Operation:     skillOperationNoop,
			CandidateRefs: []CandidateRef{"skill-other"},
			Rationale:     "unrelated noop",
		},
		{
			Operation:       skillOperationPatch,
			CandidateRefs:   []CandidateRef{candidate.Ref},
			TargetSkillID:   "skill-old",
			Description:     "updated description",
			MainFileContent: mainFile,
			Rationale:       "candidate closes the workflow gap",
		},
	}}

	got, err := buildSkillPlanProvenance(input, bundle, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("metadata count = %d, want 1", len(got))
	}
	var decoded reflectProvenanceMetadata[skillOperationProvenance]
	if err := json.Unmarshal(got[1], &decoded); err != nil {
		t.Fatal(err)
	}
	provenance := decoded.ReflectProvenance
	if provenance.OperationRef != "skill-0002" || provenance.Line != reflectLineSkill {
		t.Fatalf("unexpected provenance header: %#v", provenance.reflectProvenanceHeader)
	}
	if provenance.Reconciliation.MainFileBytes != len([]byte(mainFile)) ||
		provenance.Reconciliation.MainFileSHA256 != "0ff2fffad4515625d37ba0f15b620371b8714eb9679ab4b66a58770064a321df" {
		t.Fatalf("unexpected content digest: %#v", provenance.Reconciliation)
	}
	if strings.Contains(string(got[1]), mainFile) || strings.Contains(string(got[1]), "full old SKILL.md") {
		t.Fatal("provenance copied full skill content")
	}
}

func TestBuildFactBatchOperationsRejectsWriteWithoutCandidateRefs(t *testing.T) {
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{
			Operation:       singletonOperationCreate,
			ProposedContent: "new profile",
			Rationale:       "write without evidence",
		},
		Soul: soulSingletonWritePlan{Operation: singletonOperationNoop},
	}
	_, err := buildFactBatchOperations(factRelatedBundle{}, plan, factProvenanceInput{Context: testReflectProvenanceContext()})
	if err == nil || !strings.Contains(err.Error(), "has no candidate refs") {
		t.Fatalf("expected missing candidate refs error, got %v", err)
	}
}

func TestMarshalReflectProvenanceEnforcesByteLimit(t *testing.T) {
	empty, err := json.Marshal(reflectProvenanceMetadata[string]{ReflectProvenance: ""})
	if err != nil {
		t.Fatal(err)
	}
	exactPayload := strings.Repeat("x", maxReflectProvenanceBytes-len(empty))
	if got, err := marshalReflectProvenance(exactPayload); err != nil || len(got) != maxReflectProvenanceBytes {
		t.Fatalf("exact-limit payload len=%d err=%v", len(got), err)
	}
	_, err = marshalReflectProvenance(exactPayload + "x")
	if !errors.Is(err, errReflectProvenanceTooLarge) {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestBuildSkillPlanProvenanceRejectsSecretsInPersistedModelFields(t *testing.T) {
	const fakeSecret = "api_key=not-a-real-secret-value"
	tests := []struct {
		name   string
		mutate func(*skillCandidateDecision, *skillWriteOperation, *skillRelatedBundle)
	}{
		{
			name: "procedure prerequisites",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, _ *skillRelatedBundle) {
				decision.Candidate.Procedure.Prerequisites = []string{fakeSecret}
			},
		},
		{
			name: "evidence source URI credentials",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, _ *skillRelatedBundle) {
				decision.Candidate.Evidence[0].Source = "postgres://app:correct-horse-battery-staple@db.internal/app"
			},
		},
		{
			name: "procedure decision points",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, _ *skillRelatedBundle) {
				decision.Candidate.Procedure.DecisionPoints = []string{fakeSecret}
			},
		},
		{
			name: "procedure pitfalls",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, _ *skillRelatedBundle) {
				decision.Candidate.Procedure.Pitfalls = []string{fakeSecret}
			},
		},
		{
			name: "session skill context",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, _ *skillRelatedBundle) {
				decision.Candidate.SessionSkillContext = &sessionSkillContext{
					UsedSkillRefs:            []string{"loaded-skill"},
					ChangeAgainstLoadedSkill: fakeSecret,
				}
			},
		},
		{
			name: "evaluation rationale",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, _ *skillRelatedBundle) {
				decision.Evaluation.Rationale = fakeSecret
			},
		},
		{
			name: "related selection reason",
			mutate: func(decision *skillCandidateDecision, _ *skillWriteOperation, bundle *skillRelatedBundle) {
				bundle.RelatedRecords = []skillRelatedRecord{{
					Skill: skill.Skill{ID: "related-skill", Version: 2},
				}}
				bundle.RelationHints = []skillRelatedSelection{{
					CandidateRef: decision.Candidate.Ref,
					Related: []skillRelatedHint{{
						SkillID:  "related-skill",
						Relation: skillRelationPatchableGap,
					}},
					Reason: fakeSecret,
				}}
			},
		},
		{
			name: "reconciliation rationale",
			mutate: func(_ *skillCandidateDecision, operation *skillWriteOperation, _ *skillRelatedBundle) {
				operation.Rationale = fakeSecret
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validSkillCandidate("skill-0001")
			decision := testSkillCandidateDecision(candidate, 0.93)
			operation := skillWriteOperation{
				Operation:       skillOperationCreate,
				CandidateRefs:   []CandidateRef{candidate.Ref},
				Name:            "safe-skill",
				Description:     "safe description",
				MainFileContent: "# Safe skill\n",
				Rationale:       "safe rationale",
			}
			bundle := skillRelatedBundle{Candidates: []skillCandidate{candidate}}
			test.mutate(&decision, &operation, &bundle)

			_, err := buildSkillPlanProvenance(
				skillProvenanceInput{
					Context:   testReflectProvenanceContext(),
					Decisions: []skillCandidateDecision{decision},
				},
				bundle,
				skillReconciliationPlan{Operations: []skillWriteOperation{operation}},
			)
			if !errors.Is(err, errReflectProvenanceSecretDetected) {
				t.Fatalf("expected secret detection error, got %v", err)
			}
		})
	}
}

func TestNewReflectProvenanceContextUsesSecondPrecisionBoundary(t *testing.T) {
	from := time.Date(2026, 7, 24, 10, 11, 12, 345, time.UTC)
	to := from.Add(3*time.Second + 678*time.Nanosecond)
	got, err := newReflectProvenanceContext("session-1", "model-1", ReviewUnit{
		ReviewFromSeq:   11,
		ReviewFromAt:    from,
		LastIncludedSeq: 15,
		LastIncludedAt:  to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID == "" || got.Boundary.From.Seq != 11 || got.Boundary.To.Seq != 15 {
		t.Fatalf("unexpected context: %#v", got)
	}
	if got.Boundary.From.At != "2026-07-24T10:11:12Z" || got.Boundary.To.At != "2026-07-24T10:11:15Z" {
		t.Fatalf("unexpected timestamp precision: %#v", got.Boundary)
	}
}

func testReflectProvenanceContext() reflectProvenanceContext {
	return reflectProvenanceContext{
		RunID:     "run-1",
		SessionID: "session-1",
		ModelID:   "model-1",
		Boundary: reflectProvenanceReviewBoundary{
			From: reflectProvenanceReviewPoint{Seq: 10},
			To:   reflectProvenanceReviewPoint{Seq: 20},
		},
	}
}

func testFactCandidateDecision(candidate factCandidate, normalized float64) factCandidateDecision {
	scores := map[string]int{factScoreEvidenceStrength: 4}
	return factCandidateDecision{
		Candidate: candidate,
		Evaluation: factEvaluation{
			Ref:       candidate.Ref,
			Scores:    scores,
			Rationale: "accepted fact",
		},
		Gate: CandidateGateDecision{
			Ref:               candidate.Ref,
			NormalizedOverall: normalized,
			Scores:            scores,
		},
	}
}

func testSkillCandidateDecision(candidate skillCandidate, normalized float64) skillCandidateDecision {
	scores := map[string]int{skillScoreEvidenceStrength: 4}
	return skillCandidateDecision{
		Candidate: candidate,
		Evaluation: skillEvaluation{
			Ref:       candidate.Ref,
			Scores:    scores,
			Rationale: "accepted skill",
		},
		Gate: CandidateGateDecision{
			Ref:               candidate.Ref,
			NormalizedOverall: normalized,
			Scores:            scores,
		},
	}
}
