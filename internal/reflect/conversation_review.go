package reflect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

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
	reviewer := candidateLineReviewer{
		Stream: stream,
		Model:  model,
		Gates:  s.candidateGates,
	}
	return s.runCandidatePipeline(ctx, target, candidatePipelineOptions{
		FactLine:  reviewer.runFactLine,
		SkillLine: reviewer.runSkillLine,
	})
}

// ReviewConversationReconciledCandidates is the staged #531 entrypoint. It runs
// #532 candidate generation/evaluation and only advances line watermarks after
// #531 reconciliation and writes complete for that line.
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
	reviewer := candidateLineReviewer{
		Stream: stream,
		Model:  model,
		Gates:  s.candidateGates,
	}
	return s.runReconciliationPipeline(ctx, target, reconciliationPipelineOptions{
		FactLine:  reviewer.runFactLine,
		SkillLine: reviewer.runSkillLine,
		FactReconciler: func(ctx context.Context, target reviewTarget, unit ReviewUnit, candidates []factCandidate) error {
			return s.reconcileFactCandidates(ctx, target, unit, candidates, reviewer)
		},
		SkillReconciler: func(ctx context.Context, target reviewTarget, unit ReviewUnit, candidates []skillCandidate) error {
			return s.reconcileSkillCandidates(ctx, target, unit, candidates, reviewer)
		},
	})
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
		// No skill-disk layout: reflect runs host-identity and reads/writes skill
		// content through the DB store, which mirrors to disk itself. The reviewer
		// prompt drives create/patch, so those stay enabled — but every
		// DB read and write passes through Skill access under the review target's
		// confined identity (WithUserID+WithAgentID on reviewCtx). remove/install/
		// search/list are not exposed. The separate
		// staged reconciliation-plan writer is authorized independently.
		SkillsTool: skillstool.NewTool(s.skillStore, "", "").
			WithReadAuthorizer(s.skillReadAuthz).
			WithWriteAuthorizer(s.skillToolWriteAuthz).
			WithActionsOnly("search_installed", "load", "create", "patch"),
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
