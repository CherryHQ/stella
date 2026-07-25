package groupingest

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	reflectpkg "github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestGroupReconciliationReplacesMultipleFactsWithOneOperationVersion(t *testing.T) {
	unit, candidates, facts := reconciliationFixture()
	stream := sequentialGroupCaptureStream(t,
		groupRawToolCall(toolSubmitGroupReconciliation, `{"operations":[{
			"operation":"replace_many",
			"candidate_refs":["group-fact-0001"],
			"target_fact_ids":["fact-1","fact-2"],
			"new_content":"Coordinates all production releases.",
			"rationale":"The durable assignment consolidates both narrower responsibilities."
		}]}`),
	)
	bundle, err := BuildGroupRelatedBundle(unit, candidates, facts)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	runtimePlan, writePlan, err := (ReconciliationRunner{
		Stream: stream,
		Model:  ai.Model{ID: "test-model"},
	}).Run(context.Background(), unit, bundle)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(runtimePlan.Operations) != 1 || len(writePlan.Operations) != 1 {
		t.Fatalf("runtime=%#v write=%#v", runtimePlan, writePlan)
	}
	op := writePlan.Operations[0]
	if op.Action != memory.GroupFactActionReplaceMany ||
		op.Subject != memory.GroupFactSubjectHuman ||
		op.SubjectID != "alice" ||
		len(op.TargetFactIDs) != 2 {
		t.Fatalf("write operation = %#v", op)
	}
}

func TestGroupReconciliationNormalizesExactCreateAndReplacementToNoop(t *testing.T) {
	unit, candidates, facts := reconciliationFixture()
	candidates[0].Content = facts[0].Content
	bundle, err := BuildGroupRelatedBundle(unit, candidates, facts)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}

	for _, plan := range []GroupReconciliationPlan{
		{Operations: []GroupReconciliationOperation{{
			Operation:     memory.GroupFactActionCreate,
			CandidateRefs: []reflectpkg.CandidateRef{"group-fact-0001"},
			NewContent:    facts[0].Content,
			Rationale:     "Equivalent fact already exists.",
		}}},
		{Operations: []GroupReconciliationOperation{{
			Operation:     memory.GroupFactActionReplaceMany,
			CandidateRefs: []reflectpkg.CandidateRef{"group-fact-0001"},
			TargetFactIDs: []string{"fact-1"},
			NewContent:    facts[0].Content,
			Rationale:     "Replacement is textually unchanged.",
		}}},
	} {
		normalized, err := validateGroupReconciliationPlan(plan, unit, bundle)
		if err != nil {
			t.Fatalf("validate plan: %v", err)
		}
		op := normalized.Operations[0]
		if op.Operation != memory.GroupFactActionNoop || len(op.TargetFactIDs) != 0 || op.NewContent != "" {
			t.Fatalf("normalized operation = %#v", op)
		}
	}
}

func TestGroupReconciliationPromptRequiresMaterialChangesAndSeparateHandoffs(t *testing.T) {
	prompt := strings.Join(strings.Fields(groupFactReconciliationPrompt), " ")
	for _, phrase := range []string{
		"Wording polish, synonym substitution, tone-only rephrasing",
		"semantically entails",
		"makes obsolete a material meaning",
		"do not split it into create plus deprecate_many",
		"Cancellation decision table",
		"always use deprecate_many",
		"Never use replace_many to persist negative status text",
		"cross-participant handoff",
		"Do not write merely to cover a candidate",
	} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("reconciliation prompt missing %q", phrase)
		}
	}
}

func TestGroupReconciliationRejectsCrossSubjectTargetAndMissingCoverage(t *testing.T) {
	unit, candidates, facts := reconciliationFixture()
	facts = append(facts, memory.GroupFact{
		ID:        "fact-agent",
		GroupID:   "group-1",
		Subject:   memory.GroupFactSubjectAgent,
		SubjectID: "agent-a",
		Content:   "Handles deployment automation.",
		Status:    memory.GroupFactStatusActive,
	})
	bundle, err := BuildGroupRelatedBundle(unit, candidates, facts)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	crossSubject := GroupReconciliationPlan{Operations: []GroupReconciliationOperation{{
		Operation:     memory.GroupFactActionDeprecateMany,
		CandidateRefs: []reflectpkg.CandidateRef{"group-fact-0001"},
		TargetFactIDs: []string{"fact-agent"},
		Rationale:     "Invalid cross-subject target.",
	}}}
	if _, err := validateGroupReconciliationPlan(crossSubject, unit, bundle); err == nil {
		t.Fatal("cross-subject target should fail")
	}
	if _, err := validateGroupReconciliationPlan(GroupReconciliationPlan{}, unit, bundle); err == nil {
		t.Fatal("missing candidate coverage should fail")
	}
}

func TestGroupReconciliationRejectsDuplicateTargetEvenWhenFirstOperationNormalizesToNoop(t *testing.T) {
	unit, candidates, facts := reconciliationFixture()
	candidates = append(candidates, GroupFactCandidate{
		Ref:            "group-fact-0002",
		Subject:        memory.GroupFactSubjectHuman,
		SubjectRef:     "subject-0001",
		Content:        "No longer coordinates API releases.",
		Evidence:       []GroupFactEvidence{{Source: "Alice no longer coordinates API releases.", Reason: "Explicit invalidation."}},
		ExpectedEffect: "Stops routing API release coordination to Alice.",
	})
	bundle, err := BuildGroupRelatedBundle(unit, candidates, facts)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	plan := GroupReconciliationPlan{Operations: []GroupReconciliationOperation{
		{
			Operation:     memory.GroupFactActionReplaceMany,
			CandidateRefs: []reflectpkg.CandidateRef{"group-fact-0001"},
			TargetFactIDs: []string{"fact-1"},
			NewContent:    facts[0].Content,
			Rationale:     "Textually unchanged replacement.",
		},
		{
			Operation:     memory.GroupFactActionDeprecateMany,
			CandidateRefs: []reflectpkg.CandidateRef{"group-fact-0002"},
			TargetFactIDs: []string{"fact-1"},
			Rationale:     "The assignment was explicitly invalidated.",
		},
	}}
	if _, err := validateGroupReconciliationPlan(plan, unit, bundle); err == nil {
		t.Fatal("duplicate target should fail before operation normalization")
	}
}

func reconciliationFixture() (GroupReviewUnit, []GroupFactCandidate, []memory.GroupFact) {
	unit := GroupReviewUnit{
		GroupID: "group-1",
		Text:    "review",
		Subjects: map[string]GroupSubjectCatalogEntry{
			"subject-0001": {
				Ref:       "subject-0001",
				Subject:   memory.GroupFactSubjectHuman,
				SubjectID: "alice",
			},
		},
	}
	candidates := []GroupFactCandidate{{
		Ref:            "group-fact-0001",
		Subject:        memory.GroupFactSubjectHuman,
		SubjectRef:     "subject-0001",
		Content:        "Coordinates all production releases.",
		Evidence:       []GroupFactEvidence{{Source: "Alice owns all production releases.", Reason: "Explicit assignment."}},
		ExpectedEffect: "Routes release coordination correctly.",
	}}
	facts := []memory.GroupFact{
		{
			ID:        "fact-1",
			GroupID:   "group-1",
			Subject:   memory.GroupFactSubjectHuman,
			SubjectID: "alice",
			Content:   "Coordinates API releases.",
			Status:    memory.GroupFactStatusActive,
		},
		{
			ID:        "fact-2",
			GroupID:   "group-1",
			Subject:   memory.GroupFactSubjectHuman,
			SubjectID: "alice",
			Content:   "Coordinates database releases.",
			Status:    memory.GroupFactStatusActive,
		},
	}
	return unit, candidates, facts
}
