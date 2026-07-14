package reflect

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
)

func (s *Service) reconcileFactCandidates(ctx context.Context, target reviewTarget, _ ReviewUnit, candidates []factCandidate, runner candidateLineReviewer) error {
	facts, ok := s.memory.(memory.FactStore)
	if !ok {
		return fmt.Errorf("fact reconciliation: memory provider does not support fact reads")
	}
	// Tracing wrappers expose read interfaces but batch writes live on the
	// underlying memory provider.
	writer, ok := memory.Unwrap(s.memory).(factBatchWriter)
	if !ok {
		return fmt.Errorf("fact reconciliation: memory provider does not support fact batch writes")
	}
	var constraints memory.ConstraintStore
	if store, ok := s.memory.(memory.ConstraintStore); ok {
		constraints = store
	}

	userID := target.session.UserID
	agentID := target.session.AgentID
	bundle, err := buildFactRelatedBundle(ctx, facts, constraints, userID, agentID, candidates)
	if err != nil {
		return err
	}
	selections, err := runner.discoverKnowledgeRelations(ctx, bundle.Knowledge)
	if err != nil {
		return err
	}
	knowledge, err := attachKnowledgeRelatedRecords(bundle.Knowledge, selections)
	if err != nil {
		return err
	}
	bundle.Knowledge = knowledge
	plan, err := runner.reconcileFacts(ctx, bundle)
	if err != nil {
		return err
	}
	_, err = executeFactReconciliationPlan(ctx, writer, userID, agentID, bundle, plan)
	return err
}

func (s *Service) reconcileSkillCandidates(ctx context.Context, target reviewTarget, _ ReviewUnit, candidates []skillCandidate, runner candidateLineReviewer) error {
	bundleStore, ok := s.skillStore.(skillRelatedBundleStore)
	if !ok {
		return fmt.Errorf("skill reconciliation: skill store does not support related bundle reads")
	}
	writer, ok := s.skillStore.(reflectSkillWriter)
	if !ok {
		return fmt.Errorf("skill reconciliation: skill store does not support reflect writes")
	}

	userID := target.session.UserID
	agentID := target.session.AgentID
	catalog, err := buildSkillRelatedCatalog(ctx, bundleStore, userID, agentID)
	if err != nil {
		return err
	}
	selections, err := runner.discoverSkillRelations(ctx, candidates, catalog)
	if err != nil {
		return err
	}
	bundle, err := buildSkillRelatedBundle(ctx, bundleStore, userID, agentID, candidates, selections)
	if err != nil {
		return err
	}
	plan, err := runner.reconcileSkills(ctx, bundle)
	if err != nil {
		return err
	}
	_, err = executeSkillReconciliationPlan(ctx, writer, s.skillAuthorizer, userID, agentID, bundle, plan)
	return err
}
