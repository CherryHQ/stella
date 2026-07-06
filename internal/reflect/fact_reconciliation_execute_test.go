package reflect

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
)

func TestExecuteFactReconciliationPlanWritesReflectFactBatch(t *testing.T) {
	writer := &fakeFactBatchWriter{}
	bundle := factRelatedBundle{
		Profile: factSingletonBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectUser)},
		},
		Knowledge: knowledgeRelatedBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0002", factSubjectWorld)},
			RelatedRecords: []memory.Fact{{
				ID:      "old-world",
				Subject: memory.FactSubjectWorld,
				Status:  memory.FactStatusActive,
				Source:  memory.SourceReflect,
			}},
		},
	}
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{
			Operation:       singletonOperationCreate,
			CandidateRefs:   []CandidateRef{"fact-0001"},
			ProposedContent: "The user prefers concise Chinese replies.",
		},
		Soul: noopSoulPlan(),
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationReplaceMany,
			CandidateRefs: []CandidateRef{"fact-0002"},
			TargetFactIDs: []string{"old-world"},
			NewContent:    "New world fact.",
		}}},
	}

	if _, err := executeFactReconciliationPlan(context.Background(), writer, "user-1", "agent-1", bundle, plan); err != nil {
		t.Fatalf("executeFactReconciliationPlan: %v", err)
	}

	if writer.source != memory.SourceReflect {
		t.Fatalf("write source = %q, want reflect", writer.source)
	}
	if writer.userID != "user-1" || writer.agentID != "agent-1" {
		t.Fatalf("wrong user-agent: user=%q agent=%q", writer.userID, writer.agentID)
	}
	if len(writer.ops) != 2 {
		t.Fatalf("expected 2 batch ops, got %#v", writer.ops)
	}
	if writer.ops[0].Action != memorywrite.FactBatchSetSingleton || writer.ops[0].Subject != memory.FactSubjectUser {
		t.Fatalf("unexpected profile op: %#v", writer.ops[0])
	}
	if writer.ops[1].Action != memorywrite.FactBatchReplaceMany || writer.ops[1].TargetFactIDs[0] != "old-world" {
		t.Fatalf("unexpected knowledge op: %#v", writer.ops[1])
	}
}

func TestExecuteFactReconciliationPlanRejectsInvalidPlanBeforeWriting(t *testing.T) {
	writer := &fakeFactBatchWriter{}
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectWorld)},
		},
	}
	plan := factReconciliationPlan{
		Profile: noopSingletonPlan(),
		Soul:    noopSoulPlan(),
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationReplaceMany,
			CandidateRefs: []CandidateRef{"fact-0001"},
			TargetFactIDs: []string{"missing"},
			NewContent:    "Invalid because target is not in bundle.",
		}}},
	}

	if _, err := executeFactReconciliationPlan(context.Background(), writer, "user-1", "agent-1", bundle, plan); err == nil {
		t.Fatal("expected invalid plan error")
	}
	if writer.called {
		t.Fatal("writer should not be called for invalid plan")
	}
}

type fakeFactBatchWriter struct {
	called  bool
	source  memory.ChangeSource
	userID  string
	agentID string
	ops     []memorywrite.FactBatchOperation
}

func (w *fakeFactBatchWriter) ApplyFactBatch(ctx context.Context, userID string, agentID string, ops []memorywrite.FactBatchOperation) ([]memory.Fact, error) {
	w.called = true
	w.source = memory.ChangeSourceFromContext(ctx)
	w.userID = userID
	w.agentID = agentID
	w.ops = append([]memorywrite.FactBatchOperation(nil), ops...)
	return nil, nil
}
