package reflect

import (
	"context"
	"fmt"
	"time"
)

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
