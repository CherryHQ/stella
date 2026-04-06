// Package reflect implements background conversation review.
//
// It reviews past conversations to extract durable knowledge: user preferences,
// reusable skills, and profile updates. It replaces the old selfimprove package.
//
// Reflect is configured as a plugin row in settings_plugins (id="reflect").
// It owns its own watermark table (reflect_watermarks) for tracking review progress.
package reflect

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/skills"
	"github.com/vaayne/anna/pkg/memory"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
)

const (
	// defaultDraftMaxAge is the maximum age of a draft skill before it is
	// automatically deprecated.
	defaultDraftMaxAge = 30 * 24 * time.Hour
)

// Config holds dependencies for the reflect service.
type Config struct {
	DB        *sql.DB
	Memory    memory.Provider
	Store     config.Store
	Notifier  *channel.Dispatcher
	Workspace string
	Interval  time.Duration
	Batch     int
	Log       *slog.Logger
}

// watermarker abstracts watermark storage for testability.
type watermarker interface {
	get(ctx context.Context, sessionID string) (time.Time, error)
	set(ctx context.Context, sessionID string, at time.Time) error
}

// Service runs background conversation review.
type Service struct {
	memory    memory.Provider
	store     config.Store
	notifier  *channel.Dispatcher
	wm        watermarker
	workspace string
	interval  time.Duration
	batch     int
	log       *slog.Logger
}

// New creates a new reflect service.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 5
	}

	return &Service{
		memory:    cfg.Memory,
		store:     cfg.Store,
		notifier:  cfg.Notifier,
		wm:        newWatermarkStore(sqlc.New(cfg.DB)),
		workspace: cfg.Workspace,
		interval:  cfg.Interval,
		batch:     cfg.Batch,
		log:       cfg.Log,
	}
}

// Start runs the review loop. Blocks until ctx is cancelled.
func (s *Service) Start(ctx context.Context) error {
	s.log.Info("reflect: starting review loop", "interval", s.interval)

	s.runCycle(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.runCycle(ctx)
		}
	}
}

// ReviewNow triggers an immediate review cycle for an agent.
// Returns the number of sessions reviewed.
func (s *Service) ReviewNow(ctx context.Context, agentID string) (int, error) {
	snap, err := s.store.Snapshot(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("snapshot: %w", err)
	}
	return s.reviewAgent(ctx, snap)
}

func (s *Service) runCycle(ctx context.Context) {
	agents, err := s.store.ListEnabledAgents(ctx)
	if err != nil {
		s.log.Error("reflect: list agents", "error", err)
		return
	}

	ctx, span := startCycleSpan(ctx, len(agents))
	defer span.End()

	totalReviewed := 0
	for _, agent := range agents {
		snap, err := s.store.Snapshot(ctx, agent.ID)
		if err != nil {
			s.log.Error("reflect: snapshot", "agent", agent.ID, "error", err)
			continue
		}
		n, err := s.reviewAgent(ctx, snap)
		if err != nil {
			s.log.Error("reflect: review agent", "agent", agent.ID, "error", err)
		}
		totalReviewed += n
	}

	span.SetAttributes(
		attribute.Int("anna.reflect.sessions_reviewed", totalReviewed),
	)

	expireDrafts(s.workspace, defaultDraftMaxAge, s.log)
}

func (s *Service) reviewAgent(ctx context.Context, snap *config.Snapshot) (int, error) {
	sm, ok := s.memory.(memory.SessionManager)
	if !ok {
		return 0, nil // can't list sessions without SessionManager
	}

	ctx, span := startAgentSpan(ctx, snap.AgentID)
	defer span.End()

	candidates, err := s.listUnreviewed(ctx, sm, snap.AgentID)
	if err != nil {
		recordError(span, err)
		return 0, fmt.Errorf("list unreviewed: %w", err)
	}

	span.SetAttributes(attribute.Int("anna.reflect.candidate_count", len(candidates)))

	reviewed := 0
	for _, c := range candidates {
		if err := s.reviewConversation(ctx, snap, c); err != nil {
			s.log.Error("reflect: review conversation", "session", c.session.ID, "error", err)
			continue
		}
		reviewed++
	}

	span.SetAttributes(attribute.Int("anna.reflect.sessions_reviewed", reviewed))
	return reviewed, nil
}

// candidate represents a session that may need review.
type candidate struct {
	session    memory.Session
	lastActive time.Time // from SessionInfo — used as tiebreaker
	lastReview time.Time // zero if never reviewed
}

func (s *Service) listUnreviewed(ctx context.Context, sm memory.SessionManager, agentID string) ([]candidate, error) {
	// Fetch all non-archived sessions for this agent. We need all of them to
	// find the oldest unreviewed ones (ListInfo returns newest-first).
	sessions, err := sm.ListInfo(ctx, memory.ListOptions{
		AgentID:         agentID,
		IncludeArchived: false,
	})
	if err != nil {
		return nil, err
	}

	var candidates []candidate
	for _, sess := range sessions {
		if sess.UserID <= 0 {
			continue // skip anonymous sessions
		}

		wm, err := s.wm.get(ctx, sess.ID)
		if err != nil {
			s.log.Warn("reflect: watermark lookup failed, skipping session", "session", sess.ID, "error", err)
			continue
		}
		if !sess.LastActive.After(wm) {
			continue // already reviewed
		}

		candidates = append(candidates, candidate{
			session: memory.Session{
				ID:      sess.ID,
				AgentID: sess.AgentID,
				UserID:  sess.UserID,
				Channel: sess.Channel,
			},
			lastActive: sess.LastActive,
			lastReview: wm,
		})
	}

	// Sort oldest-first so long-unreviewed sessions get priority.
	// When lastReview times are equal (e.g. both zero for never-reviewed),
	// use lastActive as a stable tiebreaker so older sessions are reviewed first.
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := candidates[i].lastReview, candidates[j].lastReview
		if ri.Equal(rj) {
			return candidates[i].lastActive.Before(candidates[j].lastActive)
		}
		return ri.Before(rj)
	})

	if len(candidates) > s.batch {
		candidates = candidates[:s.batch]
	}
	return candidates, nil
}

func (s *Service) reviewConversation(ctx context.Context, snap *config.Snapshot, c candidate) error {
	ctx, span := startConversationSpan(ctx, c)
	defer span.End()

	userID := c.session.UserID

	// Resolve the fast-tier model and build only the needed provider.
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.API)

	span.SetAttributes(
		attribute.String("gen_ai.request.model", model.ID),
		attribute.String("gen_ai.provider.name", model.API),
	)

	reg, err := pluginproviders.BuildRegistry(model.API, pluginproviders.ProviderConfig{
		APIKey:  creds.APIKey,
		BaseURL: creds.BaseURL,
	})
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("build provider: %w", err)
	}

	// Load existing skill names for the prompt.
	userSkillsDir := filepath.Join(snap.Workspace, "users", fmt.Sprintf("%d", userID), ".agents", "skills")
	allSkills := runner.LoadSkills("", snap.Workspace, "", userSkillsDir)
	var existingNames []string
	for _, sk := range allSkills {
		existingNames = append(existingNames, sk.Name)
	}

	// Capture watermark BEFORE loading messages so concurrent writes are not skipped.
	watermark := time.Now().UTC()

	text, err := s.buildReviewContext(ctx, c.session, c.lastReview)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("build review context: %w", err)
	}

	if text == "" {
		// Nothing to review — mark reviewed anyway to advance the watermark.
		span.SetAttributes(attribute.Bool("anna.reflect.skipped", true))
		return s.wm.set(ctx, c.session.ID, watermark)
	}

	// Build the memory tool for the reviewer (profile actions only).
	reviewTool := memory.BuildTool(s.memory, memory.WithActionsOnly("profile_get", "profile_update"))

	reviewer, err := newReviewer(reviewerConfig{
		Providers:      reg,
		Model:          model,
		SkillsTool:     skills.NewTool("", snap.Workspace, "", userID),
		MemoryTool:     reviewTool,
		ExistingSkills: existingNames,
	})
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("create reviewer: %w", err)
	}

	// Inject user/agent context so the memory tool can dispatch profile operations.
	reviewCtx := memory.WithUserID(ctx, userID)
	reviewCtx = memory.WithAgentID(reviewCtx, snap.AgentID)

	result, err := reviewer.review(reviewCtx, text)
	if err != nil {
		recordError(span, err)
		return fmt.Errorf("review: %w", err) // Don't mark reviewed on error — retry next time.
	}

	span.SetAttributes(
		attribute.Int("anna.reflect.skills_mutated", result.SkillsMutated),
		attribute.Bool("anna.reflect.memory_updated", result.MemoryUpdated),
	)

	if err := s.wm.set(ctx, c.session.ID, watermark); err != nil {
		recordError(span, err)
		return fmt.Errorf("mark reviewed: %w", err)
	}

	if (result.SkillsMutated > 0 || result.MemoryUpdated) && s.notifier != nil {
		n := channel.Notification{
			Text: buildNotificationText(result),
		}
		if err := s.notifier.NotifyUser(ctx, userID, n); err != nil {
			s.log.Warn("reflect: notify", "user", userID, "error", err)
		}
	}

	s.log.Info("reflect: reviewed", "session", c.session.ID, "agent", snap.AgentID, "user", userID,
		"skills_created", result.SkillsMutated, "memory_updated", result.MemoryUpdated)

	return nil
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
