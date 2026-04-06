package selfimprove

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/pkg/memory"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour
)

// ReviewDeps holds dependencies injected by the caller.
type ReviewDeps struct {
	Memory    memory.Provider
	Store     config.Store
	Notifier  *channel.Dispatcher
	Workspace string
	Log       *slog.Logger
}

// StartReviewLoop runs the self-improvement review job on a recurring interval.
// It blocks until ctx is cancelled.
func StartReviewLoop(ctx context.Context, cfg config.SelfImproveConfig, deps ReviewDeps) {
	interval, err := time.ParseDuration(cfg.Interval())
	if err != nil {
		interval = time.Hour
	}

	if deps.Log == nil {
		deps.Log = slog.Default()
	}

	deps.Log.Info("self-improve: starting review loop", "interval", interval)

	ReviewTask(ctx, deps, cfg)
	ExpireDrafts(deps.Workspace, defaultDraftMaxAge, deps.Log)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ReviewTask(ctx, deps, cfg)
			ExpireDrafts(deps.Workspace, defaultDraftMaxAge, deps.Log)
		}
	}
}

// ReviewTask iterates over all enabled agents, finds unreviewed conversations,
// and runs the review engine on each one.
func ReviewTask(ctx context.Context, deps ReviewDeps, cfg config.SelfImproveConfig) {
	rs, ok := deps.Memory.(memory.ReviewSource)
	if !ok {
		deps.Log.Debug("self-improve: memory provider does not support review")
		return
	}

	agents, err := deps.Store.ListEnabledAgents(ctx)
	if err != nil {
		deps.Log.Error("self-improve: list agents", "error", err)
		return
	}

	for _, agent := range agents {
		reviewAgent(ctx, deps, cfg, rs, agent)
	}
}

func reviewAgent(ctx context.Context, deps ReviewDeps, cfg config.SelfImproveConfig, rs memory.ReviewSource, agent config.Agent) {
	snap, err := deps.Store.Snapshot(ctx, agent.ID)
	if err != nil {
		deps.Log.Error("self-improve: snapshot", "agent", agent.ID, "error", err)
		return
	}

	candidates, err := rs.ListUnreviewed(ctx, agent.ID, cfg.Batch())
	if err != nil {
		deps.Log.Error("self-improve: list unreviewed", "agent", agent.ID, "error", err)
		return
	}

	for _, candidate := range candidates {
		reviewConversation(ctx, deps, snap, rs, candidate)
	}
}

func reviewConversation(ctx context.Context, deps ReviewDeps, snap *config.Snapshot, rs memory.ReviewSource, candidate memory.ReviewCandidate) {
	log := deps.Log
	userID := candidate.Session.UserID

	// Resolve the fast-tier model and build only the needed provider.
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)

	reg, err := pluginproviders.BuildRegistry(model.API, pluginproviders.ProviderConfig{
		APIKey:  creds.APIKey,
		BaseURL: creds.BaseURL,
	})
	if err != nil {
		log.Error("self-improve: build provider", "error", err)
		return
	}

	// Load existing skill names for the prompt (empty annaHome skips builtin extraction).
	userSkillsDir := filepath.Join(snap.Workspace, "users", fmt.Sprintf("%d", userID), ".agents", "skills")
	allSkills := runner.LoadSkills("", snap.Workspace, "", userSkillsDir)
	var existingNames []string
	for _, s := range allSkills {
		existingNames = append(existingNames, s.Name)
	}

	// Capture the watermark before loading messages so concurrent writes
	// that arrive during the review are not skipped.
	watermark := time.Now().UTC()

	text, err := rs.BuildReviewContext(ctx, candidate.Session, candidate.LastReviewedAt)
	if err != nil {
		log.Error("self-improve: build text", "session", candidate.Session.ID, "error", err)
		return
	}

	if text == "" {
		_ = rs.MarkReviewed(ctx, candidate.Session, watermark)
		return
	}

	// Build the memory tool for the reviewer (profile actions only).
	reviewTool := memory.BuildTool(deps.Memory, memory.WithActionsOnly("soul_get", "soul_update", "profile_get", "profile_update"))

	reviewer, err := NewReviewer(ReviewerConfig{
		Providers:      reg,
		Model:          model,
		SkillsTool:     skills.NewTool("", snap.Workspace, "", userID),
		MemoryTool:     reviewTool,
		ExistingSkills: existingNames,
	})
	if err != nil {
		log.Error("self-improve: create reviewer", "session", candidate.Session.ID, "error", err)
		return
	}

	// Inject user/agent context so the memory tool can dispatch profile operations.
	reviewCtx := memory.WithUserID(ctx, userID)
	reviewCtx = memory.WithAgentID(reviewCtx, snap.AgentID)

	result, err := reviewer.Review(reviewCtx, text)
	if err != nil {
		log.Error("self-improve: review", "session", candidate.Session.ID, "error", err)
		return // Don't mark reviewed on error — retry next time.
	}

	if err := rs.MarkReviewed(ctx, candidate.Session, watermark); err != nil {
		log.Error("self-improve: mark reviewed", "session", candidate.Session.ID, "error", err)
	}

	if (result.SkillsMutated > 0 || result.MemoryUpdated) && deps.Notifier != nil {
		n := channel.Notification{
			Text: buildNotificationText(result),
		}
		if err := deps.Notifier.NotifyUser(ctx, userID, n); err != nil {
			log.Warn("self-improve: notify", "user", userID, "error", err)
		}
	}

	log.Info("self-improve: reviewed", "session", candidate.Session.ID, "agent", snap.AgentID, "user", userID,
		"skills_created", result.SkillsMutated, "memory_updated", result.MemoryUpdated)
}

// buildNotificationText constructs a user-facing notification from review results.
func buildNotificationText(r ReviewResult) string {
	var parts []string
	if r.SkillsMutated > 0 {
		parts = append(parts, fmt.Sprintf("%d new draft skill(s) extracted", r.SkillsMutated))
	}
	if r.MemoryUpdated {
		parts = append(parts, "user memory updated")
	}
	return fmt.Sprintf("Self-improvement: %s from your conversation.", strings.Join(parts, " and "))
}
