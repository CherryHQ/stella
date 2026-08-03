package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestExecuteFactReconciliationPlanWritesReflectFactBatch(t *testing.T) {
	writer := &fakeFactBatchWriter{}
	bundle := factRelatedBundle{
		Profile: factSingletonBundle{
			Candidates: []factCandidate{validFactCandidate("fact-0001", factSubjectUser)},
		},
		Knowledge: knowledgeRelatedBundle{
			Candidates: []factCandidate{
				validFactCandidate("fact-0002", factSubjectWorld),
				validFactCandidate("fact-0003", factSubjectWorld),
			},
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
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{
			{
				Operation:     knowledgeOperationReplaceMany,
				CandidateRefs: []CandidateRef{"fact-0002"},
				TargetFactIDs: []string{"old-world"},
				NewContent:    "Replacement world fact.",
			},
			{
				Operation:     knowledgeOperationCreate,
				CandidateRefs: []CandidateRef{"fact-0003"},
				NewContent:    "Independent world fact.",
			},
		}},
	}

	provenance := factProvenanceInput{
		Context: testReflectProvenanceContext(),
		Decisions: []factCandidateDecision{
			testFactCandidateDecision(bundle.Profile.Candidates[0], 0.91),
			testFactCandidateDecision(bundle.Knowledge.Candidates[0], 0.92),
			testFactCandidateDecision(bundle.Knowledge.Candidates[1], 0.93),
		},
	}
	if _, err := executeFactReconciliationPlan(context.Background(), writer, "user-1", "agent-1", bundle, plan, provenance); err != nil {
		t.Fatalf("executeFactReconciliationPlan: %v", err)
	}

	if writer.source != memory.SourceReflect {
		t.Fatalf("write source = %q, want reflect", writer.source)
	}
	if writer.userID != "user-1" || writer.agentID != "agent-1" {
		t.Fatalf("wrong user-agent: user=%q agent=%q", writer.userID, writer.agentID)
	}
	if len(writer.ops) != 3 {
		t.Fatalf("expected 3 batch ops, got %#v", writer.ops)
	}
	if writer.ops[0].Action != memorywrite.FactBatchSetSingleton || writer.ops[0].Subject != memory.FactSubjectUser {
		t.Fatalf("unexpected profile op: %#v", writer.ops[0])
	}
	if writer.ops[1].Action != memorywrite.FactBatchReplaceMany || writer.ops[1].TargetFactIDs[0] != "old-world" {
		t.Fatalf("unexpected knowledge op: %#v", writer.ops[1])
	}
	if writer.ops[2].Action != memorywrite.FactBatchCreate || writer.ops[2].Content != "Independent world fact." {
		t.Fatalf("unexpected second knowledge op: %#v", writer.ops[2])
	}
	expected := []struct {
		operationRef string
		candidateRef CandidateRef
		operation    string
	}{
		{operationRef: "profile", candidateRef: "fact-0001", operation: string(singletonOperationCreate)},
		{operationRef: "knowledge-0001", candidateRef: "fact-0002", operation: string(knowledgeOperationReplaceMany)},
		{operationRef: "knowledge-0002", candidateRef: "fact-0003", operation: string(knowledgeOperationCreate)},
	}
	for index, want := range expected {
		var metadata reflectProvenanceMetadata[factOperationProvenance]
		if err := json.Unmarshal(writer.ops[index].ChangelogMetadata, &metadata); err != nil {
			t.Fatalf("decode operation %d provenance: %v", index, err)
		}
		got := metadata.ReflectProvenance
		if got.OperationRef != want.operationRef || got.Reconciliation.Operation != want.operation ||
			len(got.Candidates) != 1 || got.Candidates[0].Ref != want.candidateRef {
			t.Fatalf("operation %d provenance = %#v, want ref=%q candidate=%q operation=%q",
				index, got, want.operationRef, want.candidateRef, want.operation)
		}
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

	if _, err := executeFactReconciliationPlan(
		context.Background(),
		writer,
		"user-1",
		"agent-1",
		bundle,
		plan,
		factProvenanceInput{Context: testReflectProvenanceContext()},
	); err == nil {
		t.Fatal("expected invalid plan error")
	}
	if writer.called {
		t.Fatal("writer should not be called for invalid plan")
	}
}

func TestExecuteFactReconciliationPlanPersistsSuccessfulProvenance(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID, agentID := seedUsageCuratorDB(t, ctx, db)
	q := sqlc.New(db)
	writer := databaseFactBatchWriter{db: db, q: q}
	candidate := validFactCandidate("fact-0001", factSubjectWorld)
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{Candidates: []factCandidate{candidate}},
	}
	plan := factReconciliationPlan{
		Profile: noopSingletonPlan(),
		Soul:    noopSoulPlan(),
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationCreate,
			CandidateRefs: []CandidateRef{candidate.Ref},
			NewContent:    "Persisted world knowledge.",
			Rationale:     "The accepted candidate is durable and useful.",
		}}},
	}
	provenance := factProvenanceInput{
		Context:   testReflectProvenanceContext(),
		Decisions: []factCandidateDecision{testFactCandidateDecision(candidate, 0.94)},
	}

	written, err := executeFactReconciliationPlan(ctx, writer, userID, agentID, bundle, plan, provenance)
	if err != nil {
		t.Fatalf("executeFactReconciliationPlan: %v", err)
	}
	if len(written) != 1 || strings.Contains(string(written[0].Metadata), "reflect_provenance") {
		t.Fatalf("unexpected written fact/entity metadata: %#v", written)
	}

	logs, err := q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID: userID, AgentID: agentID, Scope: "fact", Limit: 10,
	})
	if err != nil {
		t.Fatalf("list fact changelog: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("fact changelog count = %d, want 1", len(logs))
	}
	var metadata reflectProvenanceMetadata[factOperationProvenance]
	if err := json.Unmarshal([]byte(logs[0].Metadata.String), &metadata); err != nil {
		t.Fatalf("decode fact provenance: %v", err)
	}
	got := metadata.ReflectProvenance
	if got.RunID != provenance.Context.RunID || got.OperationRef != "knowledge-0001" ||
		got.SessionID != provenance.Context.SessionID || len(got.Candidates) != 1 ||
		got.Candidates[0].Ref != candidate.Ref {
		t.Fatalf("unexpected persisted fact provenance: %#v", got)
	}
}

func TestExecuteFactReconciliationPlanNoopDoesNotPersistProvenance(t *testing.T) {
	writer := &fakeFactBatchWriter{}
	candidate := validFactCandidate("fact-0001", factSubjectUser)
	bundle := factRelatedBundle{
		Profile: factSingletonBundle{Candidates: []factCandidate{candidate}},
	}
	plan := factReconciliationPlan{
		Profile: factSingletonWritePlan{
			Operation:     singletonOperationNoop,
			CandidateRefs: []CandidateRef{candidate.Ref},
			Rationale:     "Already represented.",
		},
		Soul: noopSoulPlan(),
	}

	if _, err := executeFactReconciliationPlan(
		context.Background(),
		writer,
		"user-1",
		"agent-1",
		bundle,
		plan,
		factProvenanceInput{Decisions: []factCandidateDecision{testFactCandidateDecision(candidate, 0.9)}},
	); err != nil {
		t.Fatalf("execute noop fact plan: %v", err)
	}
	if writer.called {
		t.Fatal("noop fact plan must not call the writer or persist provenance")
	}
}

func TestExecuteFactReconciliationPlanRejectsOversizeProvenanceBeforeWriting(t *testing.T) {
	writer := &fakeFactBatchWriter{}
	candidate := validFactCandidate("fact-0001", factSubjectWorld)
	candidate.Content = strings.Repeat("x", maxReflectProvenanceBytes)
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{Candidates: []factCandidate{candidate}},
	}
	plan := factReconciliationPlan{
		Profile: noopSingletonPlan(),
		Soul:    noopSoulPlan(),
		Knowledge: knowledgeWritePlan{Operations: []knowledgeWriteOperation{{
			Operation:     knowledgeOperationCreate,
			CandidateRefs: []CandidateRef{candidate.Ref},
			NewContent:    "oversize provenance must fail before this write",
		}}},
	}

	_, err := executeFactReconciliationPlan(
		context.Background(),
		writer,
		"user-1",
		"agent-1",
		bundle,
		plan,
		factProvenanceInput{
			Context:   testReflectProvenanceContext(),
			Decisions: []factCandidateDecision{testFactCandidateDecision(candidate, 0.9)},
		},
	)
	if !errors.Is(err, errReflectProvenanceTooLarge) {
		t.Fatalf("expected oversize provenance error, got %v", err)
	}
	if writer.called {
		t.Fatal("oversize fact provenance must fail before the batch writer")
	}
}

type fakeFactBatchWriter struct {
	called  bool
	source  memory.ChangeSource
	userID  string
	agentID string
	ops     []memorywrite.FactBatchOperation
}

type databaseFactBatchWriter struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

func (w databaseFactBatchWriter) ApplyFactBatch(ctx context.Context, userID string, agentID string, ops []memorywrite.FactBatchOperation) ([]memory.Fact, error) {
	return memorywrite.ApplyFactBatch(ctx, w.db, w.q, userID, agentID, ops)
}

func (w *fakeFactBatchWriter) ApplyFactBatch(ctx context.Context, userID string, agentID string, ops []memorywrite.FactBatchOperation) ([]memory.Fact, error) {
	w.called = true
	w.source = memory.ChangeSourceFromContext(ctx)
	w.userID = userID
	w.agentID = agentID
	w.ops = append([]memorywrite.FactBatchOperation(nil), ops...)
	return nil, nil
}
