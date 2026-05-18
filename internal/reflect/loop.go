package reflect

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// Start runs the review loop. Blocks until ctx is cancelled.
func (s *Service) Start(ctx context.Context) error {
	s.log.Info("reflect: starting review loop", "interval", s.interval)

	s.RunOnce(ctx)

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

// RunOnce executes a single review cycle.
func (s *Service) RunOnce(ctx context.Context) {
	s.runCycle(ctx)
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

	span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", totalReviewed))
	expireDrafts(s.skillStore, defaultDraftMaxAge, s.log)
}

func (s *Service) reviewAgent(ctx context.Context, snap *pkgplugins.ReflectSnapshot) (int, error) {
	sm, ok := s.memory.(memory.SessionManager)
	if !ok {
		return 0, nil
	}

	ctx, span := startAgentSpan(ctx, snap.AgentID)
	defer span.End()

	candidates, err := s.listUnreviewed(ctx, sm, snap.AgentID)
	if err != nil {
		recordError(span, err)
		return 0, fmt.Errorf("list unreviewed: %w", err)
	}

	span.SetAttributes(attribute.Int("stella.reflect.candidate_count", len(candidates)))

	reviewed := 0
	for _, c := range candidates {
		if err := s.reviewConversation(ctx, snap, c); err != nil {
			s.log.Error("reflect: review conversation", "session", c.session.ID, "error", err)
			continue
		}
		reviewed++
	}

	span.SetAttributes(attribute.Int("stella.reflect.sessions_reviewed", reviewed))
	return reviewed, nil
}
