package reflect

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
)

// RunOnce executes a single review cycle across all enabled agents.
// Returns the first cycle-level error (e.g. listing agents failed, ctx
// cancelled) so the scheduler can record the run as errored. Per-agent
// failures are logged inside runCycle and do not surface here.
func (s *Service) RunOnce(ctx context.Context) error {
	return s.runCycle(ctx)
}

// ReviewNow triggers an immediate review cycle for a single agent.
// Returns the number of sessions reviewed.
func (s *Service) ReviewNow(ctx context.Context, agentID string) (int, error) {
	snap, err := s.store.Snapshot(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("snapshot: %w", err)
	}
	return s.reviewAgent(ctx, snap)
}

func (s *Service) runCycle(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	agents, err := s.store.ListEnabledAgents(ctx)
	if err != nil {
		s.log.Error("reflect: list agents", "error", err)
		return fmt.Errorf("list enabled agents: %w", err)
	}

	ctx, span := startCycleSpan(ctx, len(agents))
	defer span.End()

	agents = s.orderAgentsForReview(ctx, agents)
	totalReviewed := 0
	processedAgents := 0
	for _, agent := range agents {
		if err := ctx.Err(); err != nil {
			s.recordNextReviewAgentCursor(ctx, agents, processedAgents)
			return err
		}
		processedAgents++
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
	s.recordNextReviewAgentCursor(ctx, agents, processedAgents)

	span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", totalReviewed))
	expireDrafts(s.skillStore, defaultDraftMaxAge, s.log)
	s.maybeRunUsageCurator(ctx)
	return nil
}

func (s *Service) reviewAgent(ctx context.Context, snap *config.Snapshot) (int, error) {
	ctx, span := startAgentSpan(ctx, snap.AgentID)
	defer span.End()

	var targets []reviewTarget
	var err error

	if s.services != nil {
		if svc := s.services.GetService(snap.AgentID); svc != nil {
			targets, err = s.listUnreviewedFromRegistry(ctx, svc.Sessions, snap.AgentID)
		}
	}
	if targets == nil && err == nil {
		// Fallback: use direct SessionManager if services not wired.
		sm, ok := s.memory.(memory.SessionManager)
		if !ok {
			return 0, nil
		}
		targets, err = s.listUnreviewed(ctx, sm, snap.AgentID)
	}
	if err != nil {
		recordError(span, err)
		return 0, fmt.Errorf("list unreviewed: %w", err)
	}

	span.SetAttributes(attribute.Int("stella.reflect.review_target_count", len(targets)))

	reviewed := 0
	batchSize := s.reviewBatchSize()
	for start := 0; start < len(targets); start += batchSize {
		end := min(start+batchSize, len(targets))
		for _, target := range targets[start:end] {
			if err := s.reviewConversation(ctx, snap, target); err != nil {
				s.log.Error("reflect: review conversation", "session", target.session.ID, "error", err)
				continue
			}
			reviewed++
		}
	}

	span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", reviewed))
	return reviewed, nil
}
