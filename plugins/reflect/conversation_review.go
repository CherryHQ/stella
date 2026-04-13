package reflect

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/vaayne/anna/pkg/ai"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	skillstool "github.com/vaayne/anna/plugins/tools/skills"
)

func (s *Service) reviewConversation(ctx context.Context, snap *pkgplugins.ReflectSnapshot, c candidate) error {
	ctx, span := startConversationSpan(ctx, c)
	defer span.End()

	userID := c.session.UserID
	model := snap.ResolveModelTier(pkgplugins.ReflectModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)

	span.SetAttributes(
		attribute.String("gen_ai.request.model", model.ID),
		attribute.String("gen_ai.provider.name", model.API),
	)

	reg, err := s.buildProviders(model.API, creds.APIKey, creds.BaseURL)
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
		span.SetAttributes(attribute.Bool("anna.reflect.skipped", true))
		return s.wm.set(ctx, c.session.ID, watermark)
	}

	reviewer, err := s.newConversationReviewer(snap, userID, model, reg)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("create reviewer: %w", err)
	}

	reviewCtx := memory.WithUserID(ctx, userID)
	reviewCtx = memory.WithAgentID(reviewCtx, snap.AgentID)
	result, err := reviewer.review(reviewCtx, text)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("review: %w", err)
	}

	span.SetAttributes(
		attribute.Int("anna.reflect.skills_mutated", result.SkillsMutated),
		attribute.Bool("anna.reflect.memory_updated", result.MemoryUpdated),
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

func (s *Service) buildProviders(api, apiKey, baseURL string) (*providers.Registry, error) {
	if s.providers == nil {
		return nil, fmt.Errorf("provider registry builder is required")
	}
	return s.providers(api, apiKey, baseURL)
}

func (s *Service) newConversationReviewer(snap *pkgplugins.ReflectSnapshot, userID int64, model ai.Model, reg *providers.Registry) (*reviewer, error) {
	return newReviewer(reviewerConfig{
		Providers:      reg,
		Model:          model,
		SkillsTool:     skillstool.NewTool("", "", snap.Workspace, "", userSkillsDir(snap.Workspace, userID), nil),
		MemoryTool:     memory.BuildTool(s.memory, memory.WithActionsOnly("profile_get", "profile_update")),
		ExistingSkills: loadExistingSkillNames(snap.Workspace, userID),
	})
}

func loadExistingSkillNames(workspace string, userID int64) []string {
	allSkills := skillstool.LoadSkills(context.Background(), skillstool.LoadSkillsConfig{
		Workspace:     workspace,
		UserSkillsDir: userSkillsDir(workspace, userID),
	})
	names := make([]string, 0, len(allSkills))
	for _, sk := range allSkills {
		names = append(names, sk.Name)
	}
	return names
}

func userSkillsDir(workspace string, userID int64) string {
	return filepath.Join(workspace, "users", fmt.Sprintf("%d", userID), ".agents", "skills")
}

func (s *Service) notifyReviewResult(ctx context.Context, userID int64, result reviewResult) {
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
