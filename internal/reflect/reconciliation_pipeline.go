package reflect

import (
	"context"
	"fmt"
)

type (
	factReconciler  func(context.Context, reviewTarget, ReviewUnit, []factCandidate) error
	skillReconciler func(context.Context, reviewTarget, ReviewUnit, []skillCandidate) error
)

type reconciliationPipelineOptions struct {
	FactLine        factCandidateLineRunner
	SkillLine       skillCandidateLineRunner
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

	if err := s.runFactReconciliationLine(ctx, target, factUnit, opts, &result); err != nil {
		result.Errors = append(result.Errors, candidateLineError{Line: reflectLineFact, Err: err})
	}
	if err := s.runSkillReconciliationLine(ctx, target, skillUnit, opts, &result); err != nil {
		result.Errors = append(result.Errors, candidateLineError{Line: reflectLineSkill, Err: err})
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
	accepted, err := opts.FactLine(ctx, unit)
	if err != nil {
		return err
	}
	result.FactAccepted = append(result.FactAccepted, accepted...)
	if len(accepted) > 0 {
		if opts.FactReconciler == nil {
			return fmt.Errorf("fact reconciler is not configured")
		}
		if err := opts.FactReconciler(ctx, target, unit, accepted); err != nil {
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
	accepted, err := opts.SkillLine(ctx, unit)
	if err != nil {
		return err
	}
	result.SkillAccepted = append(result.SkillAccepted, accepted...)
	if len(accepted) > 0 {
		if opts.SkillReconciler == nil {
			return fmt.Errorf("skill reconciler is not configured")
		}
		if err := opts.SkillReconciler(ctx, target, unit, accepted); err != nil {
			return err
		}
	}
	return s.advanceCandidateLineWatermark(ctx, target.session.ID, reflectLineSkill, unit)
}
