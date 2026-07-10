package reflect

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

func TestDecodeKnowledgeRelatedDiscoveryCall(t *testing.T) {
	got, err := decodeKnowledgeRelatedDiscoveryCall([]ai.ToolCall{
		rawToolCall("submit_knowledge_related_discovery", `{"selections":[{
			"candidate_ref":"fact-0001",
			"related":[{"fact_id":"fact-old","relation":"equivalent"}],
			"reason":"same durable fact"
		}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CandidateRef != "fact-0001" || got[0].Related[0].FactID != "fact-old" {
		t.Fatalf("unexpected selections: %#v", got)
	}
}

func TestDecodeFactReconciliationCallRejectsUnknownField(t *testing.T) {
	_, err := decodeFactReconciliationCall([]ai.ToolCall{
		rawToolCall("submit_fact_reconciliation", `{"plan":{"profile":{"operation":"noop"},"soul":{"operation":"noop"},"knowledge":{"operations":[]},"confidence":0.9}}`),
	})
	if err == nil {
		t.Fatal("expected unknown reconciliation field to be rejected")
	}
}

func TestNormalizeCandidateRefListsKeepsCrossListOverlapForValidation(t *testing.T) {
	direct, covered := normalizeCandidateRefLists(
		[]CandidateRef{"fact-0001", "fact-0001"},
		[]CandidateRef{"fact-0001", "fact-0001"},
	)

	if len(direct) != 1 || direct[0] != "fact-0001" {
		t.Fatalf("expected duplicate direct refs to be removed, got %#v", direct)
	}
	if len(covered) != 1 || covered[0] != "fact-0001" {
		t.Fatalf("expected cross-list overlap to remain visible to validation, got %#v", covered)
	}
}
