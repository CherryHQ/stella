package selfimprove

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/skills"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
)

const (
	// maxReviewMessages caps the number of messages loaded per conversation to
	// avoid exceeding the review model's context window.
	maxReviewMessages = 200

	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour
)

// ReviewDeps holds dependencies injected by the caller.
type ReviewDeps struct {
	DB        *sql.DB
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
	agents, err := deps.Store.ListEnabledAgents(ctx)
	if err != nil {
		deps.Log.Error("self-improve: list agents", "error", err)
		return
	}

	for _, agent := range agents {
		reviewAgent(ctx, deps, cfg, agent)
	}
}

func reviewAgent(ctx context.Context, deps ReviewDeps, cfg config.SelfImproveConfig, agent config.Agent) {
	snap, err := deps.Store.Snapshot(ctx, agent.ID)
	if err != nil {
		deps.Log.Error("self-improve: snapshot", "agent", agent.ID, "error", err)
		return
	}

	q := sqlc.New(deps.DB)
	conversations, err := q.ListUnreviewedConversations(ctx, sqlc.ListUnreviewedConversationsParams{
		AgentID: sql.NullString{String: agent.ID, Valid: true},
		Limit:   int64(cfg.Batch()),
	})
	if err != nil {
		deps.Log.Error("self-improve: list unreviewed", "agent", agent.ID, "error", err)
		return
	}

	for _, conv := range conversations {
		reviewConversation(ctx, deps, snap, conv)
	}
}

func reviewConversation(ctx context.Context, deps ReviewDeps, snap *config.Snapshot, conv sqlc.CtxConversation) {
	log := deps.Log
	q := sqlc.New(deps.DB)
	userID := conv.UserID.Int64

	// Resolve the fast-tier model and create a provider registry.
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)

	cfgs := make(map[string]pluginproviders.ProviderConfig, len(pluginproviders.Names()))
	for _, name := range pluginproviders.Names() {
		cfgs[name] = pluginproviders.ProviderConfig{BaseURL: creds.BaseURL}
	}
	reg := pluginproviders.BuildAll(cfgs)

	// Load existing skill names for the prompt (empty annaHome skips builtin extraction).
	userSkillsDir := filepath.Join(snap.Workspace, "users", fmt.Sprintf("%d", userID), ".agents", "skills")
	allSkills := runner.LoadSkills("", snap.Workspace, "", userSkillsDir)
	var existingNames []string
	for _, s := range allSkills {
		existingNames = append(existingNames, s.Name)
	}

	// Capture the watermark before loading messages so concurrent writes
	// that arrive during the review are not skipped.
	watermark := time.Now().UTC().Format("2006-01-02 15:04:05")

	text, err := buildConversationText(ctx, q, conv)
	if err != nil {
		log.Error("self-improve: build text", "conv", conv.ID, "error", err)
		return
	}

	if text == "" {
		_ = q.MarkConversationReviewedAt(ctx, sqlc.MarkConversationReviewedAtParams{
			SelfImproveReviewedAt: sql.NullString{String: watermark, Valid: true},
			ID:                    conv.ID,
		})
		return
	}

	memStore := memory.NewUserMemoryStore(deps.Store)
	reviewer := NewReviewer(ReviewerConfig{
		Providers:      reg,
		Model:          model,
		SkillsTool:     skills.NewTool("", snap.Workspace, "", userID),
		MemoryTool:     NewReviewMemoryTool(memStore, userID, snap.AgentID),
		ExistingSkills: existingNames,
	})
	result, err := reviewer.Review(ctx, text)
	if err != nil {
		log.Error("self-improve: review", "conv", conv.ID, "error", err)
		return // Don't mark reviewed on error — retry next time.
	}

	if err := q.MarkConversationReviewedAt(ctx, sqlc.MarkConversationReviewedAtParams{
		SelfImproveReviewedAt: sql.NullString{String: watermark, Valid: true},
		ID:                    conv.ID,
	}); err != nil {
		log.Error("self-improve: mark reviewed", "conv", conv.ID, "error", err)
	}

	if (result.SkillsMutated > 0 || result.MemoryUpdated) && deps.Notifier != nil {
		n := channel.Notification{
			Text: buildNotificationText(result),
		}
		if err := deps.Notifier.NotifyUser(ctx, userID, n); err != nil {
			log.Warn("self-improve: notify", "user", userID, "error", err)
		}
	}

	log.Info("self-improve: reviewed", "conv", conv.ID, "agent", snap.AgentID, "user", userID,
		"skills_created", result.SkillsMutated, "memory_updated", result.MemoryUpdated)
}

// buildConversationText builds the text to send to the review agent.
func buildConversationText(ctx context.Context, q *sqlc.Queries, conv sqlc.CtxConversation) (string, error) {
	var b strings.Builder

	if conv.SelfImproveReviewedAt.Valid {
		summaries, err := q.GetSummariesByConversation(ctx, conv.ID)
		if err == nil && len(summaries) > 0 {
			b.WriteString("<prior_context>\n")
			for _, s := range summaries {
				b.WriteString(memory.FormatSummaryXML(s, nil))
				b.WriteString("\n")
			}
			b.WriteString("</prior_context>\n\n")
		}

		msgs, err := q.GetMessagesSince(ctx, sqlc.GetMessagesSinceParams{
			ConversationID: conv.ID,
			CreatedAt:      conv.SelfImproveReviewedAt.String,
		})
		if err != nil {
			return "", fmt.Errorf("get messages since: %w", err)
		}
		appendMessages(&b, msgs)
	} else {
		msgs, err := q.GetMessagesByConversation(ctx, conv.ID)
		if err != nil {
			return "", fmt.Errorf("get messages: %w", err)
		}
		appendMessages(&b, msgs)
	}

	return b.String(), nil
}

func appendMessages(b *strings.Builder, msgs []sqlc.CtxMessage) {
	if len(msgs) == 0 {
		return
	}
	if len(msgs) > maxReviewMessages {
		msgs = msgs[len(msgs)-maxReviewMessages:]
	}
	for _, m := range msgs {
		fmt.Fprintf(b, "[%s] %s\n", m.Role, m.Content)
	}
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
