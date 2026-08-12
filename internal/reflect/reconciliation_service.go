package reflect

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
)

func (s *Service) reconcileFactCandidates(ctx context.Context, target reviewTarget, unit ReviewUnit, decisions []factCandidateDecision, runner candidateLineReviewer) (reconciliationWriteStats, error) {
	facts, ok := s.memory.(memory.FactStore)
	if !ok {
		return reconciliationWriteStats{}, fmt.Errorf("fact reconciliation: memory provider does not support fact reads")
	}
	// Tracing wrappers expose read interfaces but batch writes live on the
	// underlying memory provider.
	writer, ok := memory.Unwrap(s.memory).(factBatchWriter)
	if !ok {
		return reconciliationWriteStats{}, fmt.Errorf("fact reconciliation: memory provider does not support fact batch writes")
	}
	var constraints memory.ConstraintStore
	if store, ok := s.memory.(memory.ConstraintStore); ok {
		constraints = store
	}

	userID := target.session.UserID
	agentID := target.session.AgentID
	candidates := factCandidatesFromDecisions(decisions)
	bundle, err := buildFactRelatedBundle(ctx, facts, constraints, userID, agentID, candidates)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	selections, err := runner.discoverKnowledgeRelations(ctx, bundle.Knowledge)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	knowledge, err := attachKnowledgeRelatedRecords(bundle.Knowledge, selections)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	bundle.Knowledge = knowledge
	plan, err := runner.reconcileFacts(ctx, bundle)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	writes := factReconciliationWriteCount(plan)
	provenance := factProvenanceInput{Decisions: decisions}
	if writes > 0 {
		provenance.Context, err = newReflectProvenanceContext(target.session.ID, runner.Model.ID, unit)
		if err != nil {
			return reconciliationWriteStats{}, err
		}
	}
	_, err = executeFactReconciliationPlan(ctx, writer, userID, agentID, bundle, plan, provenance)
	stats := reconciliationWriteStats{Noops: factReconciliationNoopCount(bundle, plan)}
	if err == nil {
		stats.Writes = writes
	}
	return stats, err
}

func (s *Service) reconcileSkillCandidates(ctx context.Context, target reviewTarget, unit ReviewUnit, decisions []skillCandidateDecision, runner candidateLineReviewer) (reconciliationWriteStats, error) {
	bundleStore, ok := s.skillStore.(skillRelatedBundleStore)
	if !ok {
		return reconciliationWriteStats{}, fmt.Errorf("skill reconciliation: skill store does not support related bundle reads")
	}
	writer, ok := s.skillStore.(reflectSkillWriter)
	if !ok {
		return reconciliationWriteStats{}, fmt.Errorf("skill reconciliation: skill store does not support reflect writes")
	}

	userID := target.session.UserID
	agentID := target.session.AgentID
	if s.skillAuthorizer == nil {
		return reconciliationWriteStats{}, fmt.Errorf("skill reconciliation: skill authorization unavailable")
	}
	// Authorize the trusted review pair before the catalog opens any Home
	// revision. The catalog is restricted to this exact user-agent scope.
	if err := s.skillAuthorizer.AuthorizeWorkerWrite(ctx, userID, agentID, "", true); err != nil {
		return reconciliationWriteStats{}, err
	}
	candidates := skillCandidatesFromDecisions(decisions)
	catalog, err := buildSkillRelatedCatalog(ctx, bundleStore, userID, agentID)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	selections, err := runner.discoverSkillRelations(ctx, candidates, catalog)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	bundle, err := buildSkillRelatedBundle(ctx, bundleStore, userID, agentID, candidates, selections)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	plan, err := runner.reconcileSkills(ctx, bundle)
	if err != nil {
		return reconciliationWriteStats{}, err
	}
	provenance := skillProvenanceInput{Decisions: decisions}
	for _, operation := range plan.Operations {
		if operation.Operation == skillOperationNoop {
			continue
		}
		provenance.Context, err = newReflectProvenanceContext(target.session.ID, runner.Model.ID, unit)
		if err != nil {
			return reconciliationWriteStats{}, err
		}
		break
	}
	written, err := executeSkillReconciliationPlan(ctx, writer, s.skillAuthorizer, userID, agentID, bundle, plan, provenance)
	return reconciliationWriteStats{Writes: len(written), Noops: skillReconciliationNoopCount(plan)}, err
}

func factReconciliationNoopCount(bundle factRelatedBundle, plan factReconciliationPlan) int {
	count := 0
	if len(bundle.Profile.Candidates) > 0 && plan.Profile.Operation == singletonOperationNoop {
		count++
	}
	if len(bundle.Soul.Candidates) > 0 && plan.Soul.Operation == singletonOperationNoop {
		count++
	}
	for _, op := range plan.Knowledge.Operations {
		if op.Operation == knowledgeOperationNoop {
			count++
		}
	}
	return count
}

func skillReconciliationNoopCount(plan skillReconciliationPlan) int {
	count := 0
	for _, op := range plan.Operations {
		if op.Operation == skillOperationNoop {
			count++
		}
	}
	return count
}
