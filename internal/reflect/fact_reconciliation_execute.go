package reflect

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
)

type factBatchWriter interface {
	ApplyFactBatch(ctx context.Context, userID string, agentID string, ops []memorywrite.FactBatchOperation) ([]memory.Fact, error)
}

func executeFactReconciliationPlan(
	ctx context.Context,
	writer factBatchWriter,
	userID string,
	agentID string,
	bundle factRelatedBundle,
	plan factReconciliationPlan,
	provenance factProvenanceInput,
) ([]memory.Fact, error) {
	if writer == nil {
		return nil, fmt.Errorf("fact reconciliation: fact batch writer is required")
	}
	if err := validateFactReconciliationPlan(bundle, plan); err != nil {
		return nil, err
	}
	ops := factBatchOperationsFromPlan(plan)
	if len(ops) == 0 {
		return nil, nil
	}
	metadata, err := buildFactPlanProvenance(provenance, bundle, plan)
	if err != nil {
		return nil, fmt.Errorf("fact reconciliation provenance: %w", err)
	}
	if len(metadata) != len(ops) {
		return nil, fmt.Errorf("fact reconciliation provenance: metadata count %d does not match operation count %d", len(metadata), len(ops))
	}
	for index := range ops {
		ops[index].ChangelogMetadata = metadata[index]
	}
	writeCtx := memory.WithChangeSource(ctx, memory.SourceReflect)
	return writer.ApplyFactBatch(writeCtx, userID, agentID, ops)
}

func factBatchOperationsFromPlan(plan factReconciliationPlan) []memorywrite.FactBatchOperation {
	ops := make([]memorywrite.FactBatchOperation, 0, 2+len(plan.Knowledge.Operations))
	if plan.Profile.Operation != singletonOperationNoop {
		ops = append(ops, memorywrite.FactBatchOperation{
			Action:  memorywrite.FactBatchSetSingleton,
			Subject: memory.FactSubjectUser,
			Content: plan.Profile.ProposedContent,
		})
	}
	if plan.Soul.Operation != singletonOperationNoop {
		ops = append(ops, memorywrite.FactBatchOperation{
			Action:  memorywrite.FactBatchSetSingleton,
			Subject: memory.FactSubjectAgent,
			Content: plan.Soul.ProposedContent,
		})
	}
	for _, op := range plan.Knowledge.Operations {
		switch op.Operation {
		case knowledgeOperationNoop:
			continue
		case knowledgeOperationCreate:
			ops = append(ops, memorywrite.FactBatchOperation{
				Action:  memorywrite.FactBatchCreate,
				Subject: memory.FactSubjectWorld,
				Content: op.NewContent,
			})
		case knowledgeOperationReplaceMany:
			ops = append(ops, memorywrite.FactBatchOperation{
				Action:        memorywrite.FactBatchReplaceMany,
				Subject:       memory.FactSubjectWorld,
				Content:       op.NewContent,
				TargetFactIDs: append([]string(nil), op.TargetFactIDs...),
			})
		case knowledgeOperationDeprecateMany:
			ops = append(ops, memorywrite.FactBatchOperation{
				Action:        memorywrite.FactBatchDeprecateMany,
				Subject:       memory.FactSubjectWorld,
				TargetFactIDs: append([]string(nil), op.TargetFactIDs...),
			})
		}
	}
	return ops
}
