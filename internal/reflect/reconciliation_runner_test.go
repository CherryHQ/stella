package reflect

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestReconciliationRunnerDiscoversKnowledgeRelations(t *testing.T) {
	stream := sequentialCaptureStream(t,
		[]ai.ToolCall{
			rawToolCall("submit_knowledge_related_discovery", `{"selections":[{
				"candidate_ref":"fact-0001",
				"related":[{"fact_id":"fact-old","relation":"equivalent"}],
				"reason":"same durable project fact"
			}]}`),
		},
	)
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}
	bundle := knowledgeRelatedBundle{
		Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)},
		Catalog:    []factCatalogItem{{ID: "fact-old"}},
		Limits:     relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedPerCandidate},
	}

	got, err := runner.discoverKnowledgeRelations(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Related[0].FactID != "fact-old" {
		t.Fatalf("unexpected selections: %#v", got)
	}
}

func TestReconciliationRunnerRejectsInvalidKnowledgeRelations(t *testing.T) {
	stream := sequentialCaptureStream(t,
		[]ai.ToolCall{
			rawToolCall("submit_knowledge_related_discovery", `{"selections":[{
				"candidate_ref":"fact-0001",
				"related":[{"fact_id":"missing-fact","relation":"equivalent"}]
			}]}`),
		},
		[]ai.ToolCall{
			rawToolCall("submit_knowledge_related_discovery", `{"selections":[{
				"candidate_ref":"fact-0001",
				"related":[{"fact_id":"missing-fact","relation":"equivalent"}]
			}]}`),
		},
	)
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}
	bundle := knowledgeRelatedBundle{
		Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)},
		Catalog:    []factCatalogItem{{ID: "fact-old"}},
		Limits:     relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedPerCandidate},
	}

	if _, err := runner.discoverKnowledgeRelations(context.Background(), bundle); err == nil {
		t.Fatal("expected invalid relation selection to fail")
	}
}

func TestReconciliationRunnerReconcilesSkillPlan(t *testing.T) {
	stream := sequentialCaptureStream(t,
		[]ai.ToolCall{
			rawToolCall("submit_skill_reconciliation", `{"plan":{"operations":[{
				"operation":"create_skill",
				"candidate_refs":["skill-0001"],
				"name":"new-skill",
				"description":"A reflect-maintained workflow.",
				"main_file_content":"# New skill\n",
				"rationale":"No related skill covers it."
			}]}}`),
		},
	)
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}
	bundle := skillRelatedBundle{Candidates: []skillCandidate{validSkillCandidate("skill-0001")}}

	got, err := runner.reconcileSkills(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Operations) != 1 || got.Operations[0].Name != "new-skill" {
		t.Fatalf("unexpected skill plan: %#v", got)
	}
}
