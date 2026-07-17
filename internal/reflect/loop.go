package reflect

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
)

// RunOnce executes the scheduler-owned review cycle across all enabled agents.
// Production and immediate operator runs must go through the registered
// scheduler builtin (using scheduler.RunJobNow for the latter) so concurrency
// control and run records are preserved. It returns the first cycle-level error;
// per-agent failures are logged inside runCycle and do not surface here.
func (s *Service) RunOnce(ctx context.Context) error {
	return s.runCycle(ctx)
}

func (s *Service) runCycle(ctx context.Context) error {
	review, err := selectReviewTargetFunc(s.runtimeMode, s.reviewConversation, s.reviewConversationStructured)
	if err != nil {
		return err
	}
	return s.runCycleWithReviewer(ctx, review)
}

type reviewTargetFunc func(context.Context, *config.Snapshot, reviewTarget) error

func selectReviewTargetFunc(mode RuntimeMode, legacy, structured reviewTargetFunc) (reviewTargetFunc, error) {
	switch mode.withDefault() {
	case RuntimeModeLegacy:
		return legacy, nil
	case RuntimeModeStructured:
		return structured, nil
	default:
		return nil, fmt.Errorf("reflect: unsupported runtime mode %q", mode)
	}
}

func (s *Service) runCycleWithReviewer(ctx context.Context, review reviewTargetFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := s.reflectNow().Add(s.reflectRunSoftBudget())

	agents, err := s.store.ListEnabledAgents(ctx)
	if err != nil {
		s.log.Error("reflect: list agents", "error", err)
		return fmt.Errorf("list enabled agents: %w", err)
	}

	ctx, span := startCycleSpan(ctx, len(agents), s.runtimeMode.withDefault())
	defer span.End()

	agents = s.orderAgentsForReview(ctx, agents)
	totalReviewed := 0
	processedAgents := 0
	softStopped := false
	for i, agent := range agents {
		if err := ctx.Err(); err != nil {
			s.recordNextReviewAgentCursor(ctx, agents, processedAgents)
			return err
		}
		snap, err := s.store.Snapshot(ctx, agent.ID)
		if err != nil {
			s.log.Error("reflect: snapshot", "agent", agent.ID, "error", err)
			processedAgents = i + 1
			continue
		}
		n, exhausted, err := s.reviewAgentWithReviewer(ctx, snap, deadline, review)
		if err != nil {
			s.log.Error("reflect: review agent", "agent", agent.ID, "error", err)
			if ctx.Err() != nil {
				s.recordNextReviewAgentCursor(ctx, agents, i)
				return ctx.Err()
			}
		}
		totalReviewed += n
		if exhausted {
			// This agent may still have unreviewed targets. Keep the cursor here;
			// watermark ordering will omit targets that completed in this run.
			s.recordNextReviewAgentCursor(ctx, agents, i)
			softStopped = true
			break
		}
		processedAgents = i + 1
	}
	if !softStopped {
		s.recordNextReviewAgentCursor(ctx, agents, processedAgents)
	}

	span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", totalReviewed))
	s.maybeRunUsageCurator(ctx)
	return nil
}

func (s *Service) reviewAgentWithReviewer(ctx context.Context, snap *config.Snapshot, deadline time.Time, review reviewTargetFunc) (int, bool, error) {
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
			return 0, false, nil
		}
		targets, err = s.listUnreviewed(ctx, sm, snap.AgentID)
	}
	if err != nil {
		recordError(span, err)
		return 0, false, fmt.Errorf("list unreviewed: %w", err)
	}

	span.SetAttributes(attribute.Int("stella.reflect.review_target_count", len(targets)))

	reviewed := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return reviewed, false, err
		}
		if !deadline.IsZero() && !s.reflectNow().Before(deadline) {
			span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", reviewed))
			return reviewed, true, nil
		}
		if err := review(ctx, snap, target); err != nil {
			s.log.Error("reflect: review conversation", "session", target.session.ID, "error", err)
			continue
		}
		reviewed++
	}

	span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", reviewed))
	return reviewed, false, nil
}

func (s *Service) reflectNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) reflectRunSoftBudget() time.Duration {
	if s.runSoftBudget > 0 {
		return s.runSoftBudget
	}
	return defaultReflectRunSoftBudget
}
