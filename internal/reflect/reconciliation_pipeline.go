package reflect

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type (
	factCandidateDecisionLineRunner  func(context.Context, ReviewUnit) ([]factCandidateDecision, error)
	skillCandidateDecisionLineRunner func(context.Context, ReviewUnit) ([]skillCandidateDecision, error)
	factReconciler                   func(context.Context, reviewTarget, ReviewUnit, []factCandidateDecision) (reconciliationWriteStats, error)
	skillReconciler                  func(context.Context, reviewTarget, ReviewUnit, []skillCandidateDecision) (reconciliationWriteStats, error)
)

type reconciliationWriteStats struct {
	Writes int
	Noops  int
}

type reconciliationPipelineOptions struct {
	FactLine        factCandidateDecisionLineRunner
	SkillLine       skillCandidateDecisionLineRunner
	FactReconciler  factReconciler
	SkillReconciler skillReconciler
	ReviewBudget    int
}

func (s *Service) runReconciliationPipeline(ctx context.Context, target reviewTarget, opts reconciliationPipelineOptions) (candidatePipelineResult, error) {
	var result candidatePipelineResult
	budget := opts.ReviewBudget
	if budget <= 0 {
		budget = maxFallbackReviewTokens
	}

	factSince, err := s.wm.getLine(ctx, target.session.ID, reflectLineFact)
	if err != nil {
		return result, fmt.Errorf("get fact watermark: %w", err)
	}
	skillSince, err := s.wm.getLine(ctx, target.session.ID, reflectLineSkill)
	if err != nil {
		return result, fmt.Errorf("get skill watermark: %w", err)
	}
	factUnit, skillUnit, err := s.buildCandidateReviewUnits(ctx, target, factSince, skillSince, budget)
	if err != nil {
		return result, err
	}

	var factResult, skillResult candidatePipelineResult
	var factErr, skillErr error
	var lines sync.WaitGroup
	lines.Add(2)
	go func() {
		defer lines.Done()
		started := time.Now()
		factErr = s.runFactReconciliationLine(ctx, target, factUnit, opts, &factResult)
		factResult.FactStats.Duration = time.Since(started)
		factResult.FactStats.Accepted = len(factResult.FactAccepted)
		factResult.FactStats.Failed = factErr != nil
	}()
	go func() {
		defer lines.Done()
		started := time.Now()
		skillErr = s.runSkillReconciliationLine(ctx, target, skillUnit, opts, &skillResult)
		skillResult.SkillStats.Duration = time.Since(started)
		skillResult.SkillStats.Accepted = len(skillResult.SkillAccepted)
		skillResult.SkillStats.Failed = skillErr != nil
	}()
	lines.Wait()

	result = mergeCandidatePipelineResults(factResult, skillResult)
	if factErr != nil {
		result.Errors = append(result.Errors, candidateLineError{Line: reflectLineFact, Err: factErr})
	}
	if skillErr != nil {
		result.Errors = append(result.Errors, candidateLineError{Line: reflectLineSkill, Err: skillErr})
	}
	result.Skipped = dedupeReviewSkips(result.Skipped)
	if len(result.Errors) > 0 {
		return result, fmt.Errorf("reconciliation pipeline: %d line(s) failed", len(result.Errors))
	}
	return result, nil
}

func (s *Service) runFactReconciliationLine(ctx context.Context, target reviewTarget, unit ReviewUnit, opts reconciliationPipelineOptions, result *candidatePipelineResult) error {
	result.Skipped = append(result.Skipped, unit.Skipped...)
	if unit.FreshCount == 0 {
		return s.advanceCandidateLineWatermark(ctx, target.session.ID, reflectLineFact, unit)
	}
	if opts.FactLine == nil {
		return fmt.Errorf("fact candidate line is not configured")
	}
	decisions, err := opts.FactLine(ctx, unit)
	if err != nil {
		return err
	}
	result.FactAccepted = append(result.FactAccepted, factCandidatesFromDecisions(decisions)...)
	if len(decisions) > 0 {
		if opts.FactReconciler == nil {
			return fmt.Errorf("fact reconciler is not configured")
		}
		stats, err := opts.FactReconciler(ctx, target, unit, decisions)
		result.FactStats.Writes = stats.Writes
		result.FactStats.Noops = stats.Noops
		if err != nil {
			return err
		}
	}
	return s.advanceCandidateLineWatermark(ctx, target.session.ID, reflectLineFact, unit)
}

func (s *Service) runSkillReconciliationLine(ctx context.Context, target reviewTarget, unit ReviewUnit, opts reconciliationPipelineOptions, result *candidatePipelineResult) error {
	result.Skipped = append(result.Skipped, unit.Skipped...)
	if unit.FreshCount == 0 {
		return s.advanceCandidateLineWatermark(ctx, target.session.ID, reflectLineSkill, unit)
	}
	if opts.SkillLine == nil {
		return fmt.Errorf("skill candidate line is not configured")
	}
	decisions, err := opts.SkillLine(ctx, unit)
	if err != nil {
		return err
	}
	result.SkillAccepted = append(result.SkillAccepted, skillCandidatesFromDecisions(decisions)...)
	if len(decisions) > 0 {
		if opts.SkillReconciler == nil {
			return fmt.Errorf("skill reconciler is not configured")
		}
		stats, err := opts.SkillReconciler(ctx, target, unit, decisions)
		result.SkillStats.Writes = stats.Writes
		result.SkillStats.Noops = stats.Noops
		if err != nil {
			return err
		}
	}
	return s.advanceCandidateLineWatermark(ctx, target.session.ID, reflectLineSkill, unit)
}
