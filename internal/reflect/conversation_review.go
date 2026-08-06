package reflect

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

// ReviewConversationReconciledCandidates runs structured candidate generation,
// evaluation, reconciliation, and writes. A line advances only after all work
// for that line completes.
func (s *Service) ReviewConversationReconciledCandidates(ctx context.Context, snap *config.Snapshot, target reviewTarget) (candidatePipelineResult, error) {
	if snap == nil {
		return candidatePipelineResult{}, fmt.Errorf("candidate reconciliation: config snapshot is required")
	}
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)
	stream, err := s.buildStreamFunc(model.API, creds.APIKey, creds.BaseURL)
	if err != nil {
		return candidatePipelineResult{}, fmt.Errorf("build provider: %w", err)
	}
	factReviewer, factInstrumentation := instrumentCandidateReviewer(stream, model, s.candidateGates)
	skillReviewer, skillInstrumentation := instrumentCandidateReviewer(stream, model, s.candidateGates)
	result, err := s.runReconciliationPipeline(ctx, target, reconciliationPipelineOptions{
		FactLine:  factReviewer.runFactDecisionLine,
		SkillLine: skillReviewer.runSkillDecisionLine,
		FactReconciler: func(ctx context.Context, target reviewTarget, unit ReviewUnit, decisions []factCandidateDecision) (reconciliationWriteStats, error) {
			return s.reconcileFactCandidates(ctx, target, unit, decisions, factReviewer)
		},
		SkillReconciler: func(ctx context.Context, target reviewTarget, unit ReviewUnit, decisions []skillCandidateDecision) (reconciliationWriteStats, error) {
			return s.reconcileSkillCandidates(ctx, target, unit, decisions, skillReviewer)
		},
	})
	applyCandidateInstrumentation(&result, factInstrumentation, skillInstrumentation)
	return result, err
}

type candidateLineInstrumentation struct {
	llmCalls   atomic.Int64
	candidates atomic.Int64
}

func instrumentCandidateReviewer(stream providers.StreamFunc, model ai.Model, gates CandidateGateSettings) (candidateLineReviewer, *candidateLineInstrumentation) {
	instrumentation := &candidateLineInstrumentation{}
	instrumentedStream := func(ctx context.Context, model ai.Model, aiCtx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
		instrumentation.llmCalls.Add(1)
		return stream(ctx, model, aiCtx, opts)
	}
	return candidateLineReviewer{
		Stream: instrumentedStream,
		Model:  model,
		Gates:  gates,
		OnGenerated: func(count int) {
			instrumentation.candidates.Store(int64(count))
		},
	}, instrumentation
}

func applyCandidateInstrumentation(result *candidatePipelineResult, fact, skill *candidateLineInstrumentation) {
	if result == nil {
		return
	}
	result.FactStats.LLMCalls = int(fact.llmCalls.Load())
	result.FactStats.Candidates = int(fact.candidates.Load())
	result.SkillStats.LLMCalls = int(skill.llmCalls.Load())
	result.SkillStats.Candidates = int(skill.candidates.Load())
}

func (s *Service) reviewConversationStructured(ctx context.Context, snap *config.Snapshot, target reviewTarget) error {
	if !target.privateOneToOne {
		return nil
	}
	// The scheduler runs with system authority. Confine every structured read and
	// write to the target's durable owner/executor pair before entering the
	// pipeline, matching the legacy reviewer boundary.
	ctx = authz.WithUserID(ctx, target.session.UserID)
	ctx = authz.WithAgentID(ctx, target.session.AgentID)
	ctx = memory.WithChangeSource(ctx, memory.SourceReflect)
	ctx, span := startConversationSpan(ctx, target)
	defer span.End()

	result, err := s.ReviewConversationReconciledCandidates(ctx, snap, target)
	setCandidateLineSpanAttributes(span, reflectLineFact, result.FactStats)
	setCandidateLineSpanAttributes(span, reflectLineSkill, result.SkillStats)
	span.SetAttributes(
		attribute.Int("stella.reflect.fact_candidates_accepted", len(result.FactAccepted)),
		attribute.Int("stella.reflect.skill_candidates_accepted", len(result.SkillAccepted)),
		attribute.Int("stella.reflect.skipped_messages", len(result.Skipped)),
		attribute.Int("stella.reflect.line_failures", len(result.Errors)),
	)
	if err != nil {
		recordError(span, err)
		return err
	}
	s.log.Info("reflect: structured review completed",
		"session", target.session.ID,
		"agent", target.session.AgentID,
		"user", target.session.UserID,
		"fact_candidates_accepted", len(result.FactAccepted),
		"skill_candidates_accepted", len(result.SkillAccepted),
		"fact_stats", result.FactStats,
		"skill_stats", result.SkillStats,
	)
	return nil
}

func setCandidateLineSpanAttributes(span trace.Span, line reflectLine, stats candidateLineStats) {
	prefix := "stella.reflect." + string(line) + "."
	span.SetAttributes(
		attribute.Int64(prefix+"duration_ms", stats.Duration.Milliseconds()),
		attribute.Int(prefix+"llm_calls", stats.LLMCalls),
		attribute.Int(prefix+"candidates", stats.Candidates),
		attribute.Int(prefix+"accepted", stats.Accepted),
		attribute.Int(prefix+"writes", stats.Writes),
		attribute.Int(prefix+"noops", stats.Noops),
		attribute.Bool(prefix+"failed", stats.Failed),
	)
}

func (s *Service) buildStreamFunc(api, apiKey, baseURL string) (providers.StreamFunc, error) {
	if s.providers == nil {
		return nil, fmt.Errorf("provider builder is required")
	}
	return s.providers(api, apiKey, baseURL)
}
