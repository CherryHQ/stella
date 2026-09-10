//go:build personamemeval

package reflect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
)

var errBenchmarkFactReviewRetrySafe = errors.New("benchmark fact review failed before writes")

// BenchmarkFactReviewResult exposes only the aggregate diagnostics required by
// an end-to-end memory benchmark. Production candidate payloads remain private.
type BenchmarkFactReviewResult struct {
	FreshCount      int
	Generated       int
	Accepted        int
	Writes          int
	Noops           int
	LLMCalls        int
	Skipped         int
	Truncated       bool
	LastIncludedSeq int64
	WatermarkBefore int64
	WatermarkAfter  int64
	Duration        time.Duration
}

// BenchmarkReviewFactLine runs the production fact generation, evaluation,
// discovery, reconciliation, write, and watermark path without starting the
// skill line. The method exists only in PersonaMem benchmark builds.
func (s *Service) BenchmarkReviewFactLine(
	ctx context.Context,
	snapshot *config.Snapshot,
	session memory.Session,
) (BenchmarkFactReviewResult, error) {
	var output BenchmarkFactReviewResult
	if snapshot == nil {
		return output, fmt.Errorf("benchmark fact review: config snapshot is required")
	}
	if session.ID == "" || session.UserID == "" || session.AgentID == "" {
		return output, fmt.Errorf("benchmark fact review: complete session scope is required")
	}

	ctx = authz.WithUserID(ctx, session.UserID)
	ctx = authz.WithAgentID(ctx, session.AgentID)
	ctx = memory.WithChangeSource(ctx, memory.SourceReflect)
	target := reviewTarget{session: session, privateOneToOne: true}

	model := snapshot.ResolveModelTier(config.ModelTierFast)
	creds := snapshot.ResolveProviderCreds(model.API)
	stream, err := s.buildStreamFunc(model.API, creds.APIKey, creds.BaseURL)
	if err != nil {
		return output, markBenchmarkFactReviewRetrySafe(fmt.Errorf("benchmark fact review: build provider: %w", err))
	}
	reviewer, instrumentation := instrumentCandidateReviewer(stream, model, s.candidateGates)

	mark, err := s.wm.getLine(ctx, session.ID, reflectLineFact)
	if err != nil {
		return output, markBenchmarkFactReviewRetrySafe(fmt.Errorf("benchmark fact review: get watermark: %w", err))
	}
	output.WatermarkBefore = mark.Seq
	unit, err := s.buildReviewUnit(ctx, target, mark, maxFallbackReviewTokens)
	if err != nil {
		return output, markBenchmarkFactReviewRetrySafe(fmt.Errorf("benchmark fact review: build review unit: %w", err))
	}
	output.FreshCount = unit.FreshCount
	output.Skipped = len(unit.Skipped)
	output.Truncated = unit.Truncated
	output.LastIncludedSeq = unit.LastIncludedSeq

	var result candidatePipelineResult
	started := time.Now()
	err = s.runFactReconciliationLine(ctx, target, unit, reconciliationPipelineOptions{
		FactLine: func(ctx context.Context, unit ReviewUnit) ([]factCandidateDecision, error) {
			decisions, err := reviewer.runFactDecisionLine(ctx, unit)
			if err != nil {
				return nil, markBenchmarkFactReviewRetrySafe(err)
			}
			return decisions, nil
		},
		FactReconciler: func(
			ctx context.Context,
			target reviewTarget,
			unit ReviewUnit,
			decisions []factCandidateDecision,
		) (reconciliationWriteStats, error) {
			stats, err := s.reconcileFactCandidates(ctx, target, unit, decisions, reviewer)
			if isFactReconciliationPreWrite(err) {
				return stats, markBenchmarkFactReviewRetrySafe(err)
			}
			return stats, err
		},
	}, &result)
	output.Duration = time.Since(started)
	output.Generated = int(instrumentation.candidates.Load())
	output.LLMCalls = int(instrumentation.llmCalls.Load())
	output.Accepted = len(result.FactAccepted)
	output.Writes = result.FactStats.Writes
	output.Noops = result.FactStats.Noops
	if err != nil {
		return output, fmt.Errorf("benchmark fact review: %w", err)
	}

	mark, err = s.wm.getLine(ctx, session.ID, reflectLineFact)
	if err != nil {
		// The reconciliation may already have committed, so this failure is not
		// safe for the benchmark harness to retry.
		return output, fmt.Errorf("benchmark fact review: read advanced watermark: %w", err)
	}
	output.WatermarkAfter = mark.Seq
	return output, nil
}

// BenchmarkFactReviewRetrySafe reports whether a review failed before entering
// the fact batch executor. Callers must additionally require a transient error.
func BenchmarkFactReviewRetrySafe(err error) bool {
	return errors.Is(err, errBenchmarkFactReviewRetrySafe)
}

func markBenchmarkFactReviewRetrySafe(err error) error {
	return fmt.Errorf("%w: %w", errBenchmarkFactReviewRetrySafe, err)
}
