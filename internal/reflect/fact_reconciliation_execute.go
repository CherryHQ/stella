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
	ops, err := buildFactBatchOperations(bundle, plan, provenance)
	if err != nil {
		return nil, fmt.Errorf("fact reconciliation provenance: %w", err)
	}
	if len(ops) == 0 {
		return nil, nil
	}
	writeCtx := memory.WithChangeSource(ctx, memory.SourceReflect)
	return writer.ApplyFactBatch(writeCtx, userID, agentID, ops)
}

func buildFactBatchOperations(bundle factRelatedBundle, plan factReconciliationPlan, provenance factProvenanceInput) ([]memorywrite.FactBatchOperation, error) {
	ops := make([]memorywrite.FactBatchOperation, 0, 2+len(plan.Knowledge.Operations))
	if plan.Profile.Operation != singletonOperationNoop {
		metadata, err := buildFactOperationProvenance(
			provenance,
			"profile",
			string(plan.Profile.Operation),
			plan.Profile.CandidateRefs,
			plan.Profile.CoveredCandidateRefs,
			nil,
			nil,
			plan.Profile.Rationale,
		)
		if err != nil {
			return nil, err
		}
		ops = append(ops, memorywrite.FactBatchOperation{
			Action:            memorywrite.FactBatchSetSingleton,
			Subject:           memory.FactSubjectUser,
			Content:           plan.Profile.ProposedContent,
			ChangelogMetadata: metadata,
		})
	}
	if plan.Soul.Operation != singletonOperationNoop {
		metadata, err := buildFactOperationProvenance(
			provenance,
			"soul",
			string(plan.Soul.Operation),
			plan.Soul.CandidateRefs,
			plan.Soul.CoveredCandidateRefs,
			nil,
			nil,
			plan.Soul.Rationale,
		)
		if err != nil {
			return nil, err
		}
		ops = append(ops, memorywrite.FactBatchOperation{
			Action:            memorywrite.FactBatchSetSingleton,
			Subject:           memory.FactSubjectAgent,
			Content:           plan.Soul.ProposedContent,
			ChangelogMetadata: metadata,
		})
	}
	for index, operation := range plan.Knowledge.Operations {
		if operation.Operation == knowledgeOperationNoop {
			continue
		}
		var batchOperation memorywrite.FactBatchOperation
		switch operation.Operation {
		case knowledgeOperationCreate:
			batchOperation = memorywrite.FactBatchOperation{
				Action:  memorywrite.FactBatchCreate,
				Subject: memory.FactSubjectWorld,
				Content: operation.NewContent,
			}
		case knowledgeOperationReplaceMany:
			batchOperation = memorywrite.FactBatchOperation{
				Action:        memorywrite.FactBatchReplaceMany,
				Subject:       memory.FactSubjectWorld,
				Content:       operation.NewContent,
				TargetFactIDs: append([]string(nil), operation.TargetFactIDs...),
			}
		case knowledgeOperationDeprecateMany:
			batchOperation = memorywrite.FactBatchOperation{
				Action:        memorywrite.FactBatchDeprecateMany,
				Subject:       memory.FactSubjectWorld,
				TargetFactIDs: append([]string(nil), operation.TargetFactIDs...),
			}
		default:
			return nil, fmt.Errorf("unsupported knowledge operation %q", operation.Operation)
		}
		metadata, err := buildFactOperationProvenance(
			provenance,
			fmt.Sprintf("knowledge-%04d", index+1),
			string(operation.Operation),
			operation.CandidateRefs,
			operation.CoveredCandidateRefs,
			operation.TargetFactIDs,
			&bundle.Knowledge,
			operation.Rationale,
		)
		if err != nil {
			return nil, err
		}
		batchOperation.ChangelogMetadata = metadata
		ops = append(ops, batchOperation)
	}
	return ops, nil
}

func factReconciliationWriteCount(plan factReconciliationPlan) int {
	count := 0
	if plan.Profile.Operation != singletonOperationNoop {
		count++
	}
	if plan.Soul.Operation != singletonOperationNoop {
		count++
	}
	for _, operation := range plan.Knowledge.Operations {
		if operation.Operation != knowledgeOperationNoop {
			count++
		}
	}
	return count
}
