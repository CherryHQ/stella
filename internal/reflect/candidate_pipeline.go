package reflect

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type (
	factCandidateLineRunner  func(context.Context, ReviewUnit) ([]factCandidate, error)
	skillCandidateLineRunner func(context.Context, ReviewUnit) ([]skillCandidate, error)
)

type candidatePipelineOptions struct {
	FactLine     factCandidateLineRunner
	SkillLine    skillCandidateLineRunner
	ReviewBudget int
}

type candidateLineError struct {
	Line reflectLine
	Err  error
}

func (e candidateLineError) Error() string {
	return fmt.Sprintf("%s line: %v", e.Line, e.Err)
}

func (e candidateLineError) Unwrap() error {
	return e.Err
}

type candidatePipelineResult struct {
	FactAccepted  []factCandidate
	SkillAccepted []skillCandidate
	FactStats     candidateLineStats
	SkillStats    candidateLineStats
	Skipped       []ReviewSkip
	Errors        []candidateLineError
}

type candidateLineStats struct {
	Duration   time.Duration
	LLMCalls   int
	Candidates int
	Accepted   int
	Writes     int
	Noops      int
	Failed     bool
}

func (s *Service) runCandidatePipeline(ctx context.Context, target reviewTarget, opts candidatePipelineOptions) (candidatePipelineResult, error) {
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
		factErr = s.runFactCandidateLine(ctx, target.session.ID, factUnit, opts.FactLine, &factResult)
		factResult.FactStats.Duration = time.Since(started)
		factResult.FactStats.Accepted = len(factResult.FactAccepted)
		factResult.FactStats.Failed = factErr != nil
	}()
	go func() {
		defer lines.Done()
		started := time.Now()
		skillErr = s.runSkillCandidateLine(ctx, target.session.ID, skillUnit, opts.SkillLine, &skillResult)
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
		return result, fmt.Errorf("candidate pipeline: %d line(s) failed", len(result.Errors))
	}
	return result, nil
}

func mergeCandidatePipelineResults(results ...candidatePipelineResult) candidatePipelineResult {
	var merged candidatePipelineResult
	for _, result := range results {
		merged.FactAccepted = append(merged.FactAccepted, result.FactAccepted...)
		merged.SkillAccepted = append(merged.SkillAccepted, result.SkillAccepted...)
		merged.FactStats = mergeCandidateLineStats(merged.FactStats, result.FactStats)
		merged.SkillStats = mergeCandidateLineStats(merged.SkillStats, result.SkillStats)
		merged.Skipped = append(merged.Skipped, result.Skipped...)
	}
	return merged
}

func mergeCandidateLineStats(left, right candidateLineStats) candidateLineStats {
	return candidateLineStats{
		Duration:   left.Duration + right.Duration,
		LLMCalls:   left.LLMCalls + right.LLMCalls,
		Candidates: left.Candidates + right.Candidates,
		Accepted:   left.Accepted + right.Accepted,
		Writes:     left.Writes + right.Writes,
		Noops:      left.Noops + right.Noops,
		Failed:     left.Failed || right.Failed,
	}
}

func (s *Service) buildCandidateReviewUnits(ctx context.Context, target reviewTarget, factSince, skillSince reviewWatermark, budget int) (ReviewUnit, ReviewUnit, error) {
	if factSince == skillSince {
		unit, err := s.buildReviewUnit(ctx, target, factSince, budget)
		if err != nil {
			return ReviewUnit{}, ReviewUnit{}, fmt.Errorf("build shared review unit: %w", err)
		}
		return unit, unit, nil
	}

	factUnit, err := s.buildReviewUnit(ctx, target, factSince, budget)
	if err != nil {
		return ReviewUnit{}, ReviewUnit{}, fmt.Errorf("build fact review unit: %w", err)
	}
	skillUnit, err := s.buildReviewUnit(ctx, target, skillSince, budget)
	if err != nil {
		return ReviewUnit{}, ReviewUnit{}, fmt.Errorf("build skill review unit: %w", err)
	}
	return factUnit, skillUnit, nil
}

func (s *Service) runFactCandidateLine(ctx context.Context, sessionID string, unit ReviewUnit, runner factCandidateLineRunner, result *candidatePipelineResult) error {
	result.Skipped = append(result.Skipped, unit.Skipped...)
	if unit.FreshCount == 0 {
		return s.advanceCandidateLineWatermark(ctx, sessionID, reflectLineFact, unit)
	}
	if runner == nil {
		return fmt.Errorf("fact candidate line is not configured")
	}
	accepted, err := runner(ctx, unit)
	if err != nil {
		return err
	}
	result.FactAccepted = append(result.FactAccepted, accepted...)
	return s.advanceCandidateLineWatermark(ctx, sessionID, reflectLineFact, unit)
}

func (s *Service) runSkillCandidateLine(ctx context.Context, sessionID string, unit ReviewUnit, runner skillCandidateLineRunner, result *candidatePipelineResult) error {
	result.Skipped = append(result.Skipped, unit.Skipped...)
	if unit.FreshCount == 0 {
		return s.advanceCandidateLineWatermark(ctx, sessionID, reflectLineSkill, unit)
	}
	if runner == nil {
		return fmt.Errorf("skill candidate line is not configured")
	}
	accepted, err := runner(ctx, unit)
	if err != nil {
		return err
	}
	result.SkillAccepted = append(result.SkillAccepted, accepted...)
	return s.advanceCandidateLineWatermark(ctx, sessionID, reflectLineSkill, unit)
}

func (s *Service) advanceCandidateLineWatermark(ctx context.Context, sessionID string, line reflectLine, unit ReviewUnit) error {
	if unit.LastIncludedSeq == 0 && unit.LastIncludedAt.IsZero() {
		return nil
	}
	return s.wm.setLine(ctx, sessionID, line, reviewWatermark{
		Seq: unit.LastIncludedSeq,
		At:  unit.LastIncludedAt,
	})
}

func dedupeReviewSkips(skips []ReviewSkip) []ReviewSkip {
	if len(skips) <= 1 {
		return skips
	}
	type skipKey struct {
		Reason   ReviewSkipReason
		Role     string
		At       string
		FirstSeq int64
		LastSeq  int64
		Size     int
	}
	seen := make(map[skipKey]struct{}, len(skips))
	out := make([]ReviewSkip, 0, len(skips))
	for _, skip := range skips {
		key := skipKey{
			Reason:   skip.Reason,
			Role:     skip.Role,
			At:       skip.At.UTC().Format(time.RFC3339Nano),
			FirstSeq: skip.FirstSeq,
			LastSeq:  skip.LastSeq,
			Size:     skip.Size,
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, skip)
	}
	return out
}
