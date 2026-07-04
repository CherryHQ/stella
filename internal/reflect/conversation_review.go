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

func (s *Service) reviewConversation(ctx context.Context, snap *config.Snapshot, c candidate) error {
	ctx, span := startConversationSpan(ctx, c)
	defer span.End()

	userID := c.session.UserID
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
	text, err := s.buildReviewContext(ctx, c.session, c.lastReview)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("build review context: %w", err)
	}
	if text == "" {
		span.SetAttributes(attribute.Bool("stella.reflect.skipped", true))
		return s.wm.set(ctx, c.session.ID, watermark)
	}

	reviewer, err := s.newConversationReviewer(ctx, snap, userID, model, stream)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("create reviewer: %w", err)
	}

	reviewCtx := authz.WithUserID(ctx, userID)
	reviewCtx = authz.WithAgentID(reviewCtx, snap.AgentID)
	reviewCtx = memory.WithChangeSource(reviewCtx, memory.SourceReflect)
	result, err := reviewer.review(reviewCtx, text)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("review: %w", err)
	}

	span.SetAttributes(
		attribute.Int("stella.reflect.skills_mutated", result.SkillsMutated),
		attribute.Bool("stella.reflect.memory_updated", result.MemoryUpdated),
	)

	if err := s.wm.set(ctx, c.session.ID, watermark); err != nil {
		recordError(span, err)
		return fmt.Errorf("mark reviewed: %w", err)
	}

	s.notifyReviewResult(ctx, userID, result)
	s.log.Info("reflect: reviewed", "session", c.session.ID, "agent", snap.AgentID, "user", userID,
		"skills_created", result.SkillsMutated, "memory_updated", result.MemoryUpdated)

	return nil
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
		// No skill-disk layout: reflect runs host-identity and only reads skill
		// content (from the DB) and writes back through the store, which mirrors to
		// disk itself. The tool needs no skill-path knowledge here.
		SkillsTool:     skillstool.NewTool(s.skillStore, "", ""),
		MemoryTool:     memory.BuildTool(s.memory, memory.WithActionsOnly("profile_get", "profile_update")),
		ExistingSkills: loadExistingSkillSummaries(context.Background(), s.skillStore, userID),
		CurrentProfile: profile,
	})
}

func loadExistingSkillSummaries(ctx context.Context, store pkgplugins.SkillStore, userID string) []string {
	if store == nil {
		return nil
	}
	vc := pkgplugins.SkillViewContext{UserID: userID}
	all, err := store.List(ctx, vc)
	if err != nil {
		return nil
	}
	entries := make([]string, 0, len(all))
	for _, sk := range all {
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
		parts = append(parts, fmt.Sprintf("%d new draft skill(s) extracted", r.SkillsMutated))
	}
	if r.MemoryUpdated {
		parts = append(parts, "user memory updated")
	}
	return fmt.Sprintf("Self-improvement: %s from your conversation.", strings.Join(parts, " and "))
}
