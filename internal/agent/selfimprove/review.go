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
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/ai/providers/anthropic"
	"github.com/vaayne/anna/internal/ai/providers/openai"
	openairesponse "github.com/vaayne/anna/internal/ai/providers/openai-response"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/memory"
)

// ReviewDeps holds dependencies injected by the caller.
type ReviewDeps struct {
	DB        *sql.DB
	Store     config.Store
	Notifier  *channel.Dispatcher
	Workspace string // agent workspace root for ExpireDrafts
	Log       *slog.Logger
}

// newProviderRegistry creates a provider registry containing only the adapter
// required by the fast-tier model, configured with the correct base URL.
func newProviderRegistry(snap *config.Snapshot) ai.ProviderGetter {
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)

	reg := ai.NewRegistry()
	switch model.API {
	case "anthropic":
		reg.Register(anthropic.New(anthropic.Config{BaseURL: creds.BaseURL}))
	case "openai":
		reg.Register(openai.New(openai.Config{BaseURL: creds.BaseURL}))
	case "openai-response":
		reg.Register(openairesponse.New(openairesponse.Config{BaseURL: creds.BaseURL}))
	default:
		// Fall back to registering all providers for unknown API types.
		reg.Register(anthropic.New(anthropic.Config{BaseURL: creds.BaseURL}))
		reg.Register(openai.New(openai.Config{BaseURL: creds.BaseURL}))
		reg.Register(openairesponse.New(openairesponse.Config{BaseURL: creds.BaseURL}))
	}
	return reg
}

// StartReviewLoop runs the self-improvement review job on a recurring interval.
// It blocks until ctx is cancelled.
func StartReviewLoop(ctx context.Context, cfg config.SelfImproveConfig, deps ReviewDeps) {
	interval, err := time.ParseDuration(cfg.Interval())
	if err != nil {
		interval = time.Hour
	}

	log := deps.Log
	if log == nil {
		log = slog.Default()
	}

	log.Info("self-improve: starting review loop", "interval", interval)

	// Run once immediately so users don't wait a full interval after restart.
	ReviewTask(ctx, deps, cfg)
	ExpireDrafts(deps.Workspace, 30*24*time.Hour, log)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ReviewTask(ctx, deps, cfg)
			ExpireDrafts(deps.Workspace, 30*24*time.Hour, log)
		}
	}
}

// ReviewTask iterates over all enabled agents, finds unreviewed conversations,
// and runs the review engine on each one.
func ReviewTask(ctx context.Context, deps ReviewDeps, cfg config.SelfImproveConfig) {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}

	q := sqlc.New(deps.DB)

	agents, err := deps.Store.ListEnabledAgents(ctx)
	if err != nil {
		log.Error("self-improve: list agents", "error", err)
		return
	}

	for _, agent := range agents {
		reviewAgent(ctx, deps, cfg, q, log, agent)
	}
}

func reviewAgent(ctx context.Context, deps ReviewDeps, cfg config.SelfImproveConfig, q *sqlc.Queries, log *slog.Logger, agent config.Agent) {
	snap, err := deps.Store.Snapshot(ctx, agent.ID)
	if err != nil {
		log.Error("self-improve: snapshot", "agent", agent.ID, "error", err)
		return
	}

	model := snap.ResolveModelTier(config.ModelTierFast)
	providers := newProviderRegistry(snap)

	conversations, err := q.ListUnreviewedConversations(ctx, sqlc.ListUnreviewedConversationsParams{
		AgentID: sql.NullString{String: agent.ID, Valid: true},
		Limit:   int64(cfg.Batch()),
	})
	if err != nil {
		log.Error("self-improve: list unreviewed", "agent", agent.ID, "error", err)
		return
	}

	for _, conv := range conversations {
		reviewConversation(ctx, deps, q, log, snap, providers, model, conv)
	}
}

func reviewConversation(ctx context.Context, deps ReviewDeps, q *sqlc.Queries, log *slog.Logger, snap *config.Snapshot, providers ai.ProviderGetter, model ai.Model, conv sqlc.CtxConversation) {
	userID := conv.UserID.Int64
	userSkillsDir := filepath.Join(snap.Workspace, "users", fmt.Sprintf("%d", userID), ".agents", "skills")

	skills := runner.LoadSkills(config.AnnaHome(), snap.Workspace, "", userSkillsDir)
	var existingNames []string
	for _, s := range skills {
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

	reviewer := NewReviewer(providers, model, userSkillsDir, existingNames)
	created, err := reviewer.Review(ctx, text)
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

	if created > 0 && deps.Notifier != nil {
		n := channel.Notification{
			Text: fmt.Sprintf("Self-improvement: %d new draft skill(s) extracted from your conversation. Use the skills tool to review and enable them.", created),
		}
		if err := deps.Notifier.NotifyUser(ctx, userID, n); err != nil {
			log.Warn("self-improve: notify", "user", userID, "error", err)
		}
	}

	log.Info("self-improve: reviewed", "conv", conv.ID, "agent", snap.AgentID, "user", userID, "skills_created", created)
}

// maxReviewMessages caps the number of messages loaded per conversation to
// avoid exceeding the review model's context window.
const maxReviewMessages = 200

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
		if len(msgs) == 0 {
			return "", nil
		}
		if len(msgs) > maxReviewMessages {
			msgs = msgs[len(msgs)-maxReviewMessages:]
		}
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content)
		}
	} else {
		msgs, err := q.GetMessagesByConversation(ctx, conv.ID)
		if err != nil {
			return "", fmt.Errorf("get messages: %w", err)
		}
		if len(msgs) == 0 {
			return "", nil
		}
		if len(msgs) > maxReviewMessages {
			msgs = msgs[len(msgs)-maxReviewMessages:]
		}
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content)
		}
	}

	return b.String(), nil
}
