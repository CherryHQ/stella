package reflect

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

func (s *Service) reviewConversation(ctx context.Context, snap *config.Snapshot, target reviewTarget) error {
	// The production reviewer predates the candidate pipeline, whose
	// buildReviewUnit also enforces this boundary. Keep the live path fail-closed:
	// group history is assembled from the shared event log and must not be mined
	// as a private one-to-one conversation.
	if !target.privateOneToOne {
		return nil
	}

	ctx, span := startConversationSpan(ctx, target)
	defer span.End()

	userID := target.session.UserID
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)

	span.SetAttributes(
		attribute.String("gen_ai.request.model", model.ID),
		attribute.String("gen_ai.provider.name", model.API),
	)

	stream, err := s.buildStreamFunc(model.API, creds.APIKey, creds.BaseURL)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("build provider: %w", err)
	}

	watermark := time.Now().UTC()
	text, err := s.buildReviewContext(ctx, target.session, target.lastReview)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("build review context: %w", err)
	}
	if text == "" {
		span.SetAttributes(attribute.Bool("stella.reflect.skipped", true))
		return s.wm.set(ctx, target.session.ID, watermark)
	}

	// Build the trusted review identity up front so existing-skill summaries and
	// tool reads/writes all use the same confined user+agent identity, never a
	// context.Background bypass.
	reviewCtx := authz.WithUserID(ctx, userID)
	reviewCtx = authz.WithAgentID(reviewCtx, snap.AgentID)
	reviewCtx = memory.WithChangeSource(reviewCtx, memory.SourceReflect)

	reviewer, err := s.newConversationReviewer(reviewCtx, snap, userID, model, stream)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("create reviewer: %w", err)
	}

	result, err := reviewer.review(reviewCtx, text)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("review: %w", err)
	}

	span.SetAttributes(
		attribute.Int("stella.reflect.skills_mutated", result.SkillsMutated),
		attribute.Bool("stella.reflect.memory_updated", result.MemoryUpdated),
	)

	if err := s.wm.set(ctx, target.session.ID, watermark); err != nil {
		recordError(span, err)
		return fmt.Errorf("mark reviewed: %w", err)
	}

	s.notifyReviewResult(ctx, userID, result)
	s.log.Info("reflect: reviewed", "session", target.session.ID, "agent", snap.AgentID, "user", userID,
		"skills_created", result.SkillsMutated, "memory_updated", result.MemoryUpdated)

	return nil
}

// reviewConversationCandidates is staged #532 plumbing. It stays out of the
// production review loop until #531 can persist/reconcile accepted candidates;
// otherwise this path would advance line watermarks and then drop the output.
func (s *Service) reviewConversationCandidates(ctx context.Context, snap *config.Snapshot, target reviewTarget) (candidatePipelineResult, error) {
	if snap == nil {
		return candidatePipelineResult{}, fmt.Errorf("candidate review: config snapshot is required")
	}
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)
	stream, err := s.buildStreamFunc(model.API, creds.APIKey, creds.BaseURL)
	if err != nil {
		return candidatePipelineResult{}, fmt.Errorf("build provider: %w", err)
	}
	factReviewer, factInstrumentation := instrumentCandidateReviewer(stream, model, s.candidateGates)
	skillReviewer, skillInstrumentation := instrumentCandidateReviewer(stream, model, s.candidateGates)
	result, err := s.runCandidatePipeline(ctx, target, candidatePipelineOptions{
		FactLine:  factReviewer.runFactLine,
		SkillLine: skillReviewer.runSkillLine,
	})
	applyCandidateInstrumentation(&result, factInstrumentation, skillInstrumentation)
	return result, err
}

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
		FactLine:  factReviewer.runFactLine,
		SkillLine: skillReviewer.runSkillLine,
		FactReconciler: func(ctx context.Context, target reviewTarget, unit ReviewUnit, candidates []factCandidate) (reconciliationWriteStats, error) {
			return s.reconcileFactCandidates(ctx, target, unit, candidates, factReviewer)
		},
		SkillReconciler: func(ctx context.Context, target reviewTarget, unit ReviewUnit, candidates []skillCandidate) (reconciliationWriteStats, error) {
			return s.reconcileSkillCandidates(ctx, target, unit, candidates, skillReviewer)
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
	ctx, span := startConversationSpan(ctx, target)
	defer span.End()
	span.SetAttributes(attribute.String("stella.reflect.mode", string(RuntimeModeStructured)))

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

func (s *Service) newConversationReviewer(ctx context.Context, snap *config.Snapshot, userID string, model ai.Model, stream providers.StreamFunc) (*reviewer, error) {
	var profile string
	if ps, ok := s.memory.(memory.ProfileStore); ok {
		profile, _ = ps.GetProfile(ctx, userID, snap.AgentID)
	}
	return newReviewer(reviewerConfig{
		Stream: stream,
		Model:  model,
		// No skill-disk layout: reflect runs host-identity and reads/writes Skill
		// content through PostgreSQL. Runtime loads materialize their own derived
		// cache. The reviewer prompt drives create/patch/deprecate, so those stay
		// enabled — but every
		// DB read and write passes through Skill access under the review target's
		// confined identity (WithUserID+WithAgentID on reviewCtx). remove/install/
		// search/list are not exposed. The separate
		// staged reconciliation-plan writer is authorized independently.
		SkillsTool: skillstool.NewTool(s.skillStore, "", "").
			WithReadAuthorizer(s.skillReadAuthz).
			WithWriteAuthorizer(s.skillToolWriteAuthz).
			WithActionsOnly("search_installed", "load", "create", "patch", "deprecate"),
		MemoryTool:     memory.BuildTool(s.memory, memory.WithActionsOnly("profile_get", "profile_update")),
		ExistingSkills: s.loadExistingSkillSummaries(ctx, userID),
		CurrentProfile: profile,
	})
}

// loadExistingSkillSummaries lists the reviewer's visible skills for the prompt.
// It runs under the confined review identity (ctx carries WithUserID+WithAgentID)
// and filters every DB row through the same Skill read rules as the tool. A
// revoked owner or agent binding hides the skill from both the prompt and
// load/search. Authorization failures fail hidden rather than leaking a row.
func (s *Service) loadExistingSkillSummaries(ctx context.Context, userID string) []string {
	if s.skillStore == nil {
		return nil
	}
	all, err := s.skillStore.List(ctx, pkgplugins.SkillViewContext{UserID: userID})
	if err != nil {
		return nil
	}
	var dec skillstool.SkillReadDecision
	if s.skillReadAuthz != nil {
		if d, err := s.skillReadAuthz.BeginRead(ctx); err == nil {
			dec = d
		}
	}
	entries := make([]string, 0, len(all))
	for _, sk := range all {
		// store.List returns DB rows; each must pass the read decision. Without a
		// decider (unavailable/no identity) drop it — never leak an unauthorized row.
		if dec == nil {
			continue
		}
		allowed, err := dec.AllowRead(ctx, sk.ID, sk.Scope, sk.UserID, sk.AgentID)
		if err != nil || !allowed {
			continue
		}
		if sk.Description != "" {
			entries = append(entries, sk.Name+" — "+sk.Description)
		} else {
			entries = append(entries, sk.Name)
		}
	}
	return entries
}

func (s *Service) notifyReviewResult(ctx context.Context, userID string, result reviewResult) {
	if (result.SkillsMutated <= 0 && !result.MemoryUpdated) || s.notifier == nil {
		return
	}

	n := pkgchannel.Notification{Text: buildNotificationText(result)}
	if err := s.notifier.NotifyUser(ctx, userID, n); err != nil {
		s.log.Warn("reflect: notify", "user", userID, "error", err)
	}
}

func buildNotificationText(r reviewResult) string {
	var parts []string
	if r.SkillsMutated > 0 {
		parts = append(parts, fmt.Sprintf("%d new skill(s) extracted", r.SkillsMutated))
	}
	if r.MemoryUpdated {
		parts = append(parts, "user memory updated")
	}
	return fmt.Sprintf("Self-improvement: %s from your conversation.", strings.Join(parts, " and "))
}
