package goal

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// MaterializeFrozenLayer installs an already-approved plan without entering the
// planner approval gate. It is intentionally narrow: workflow replay supplies
// the frozen content, while goal keeps the idempotency fence and release rules.
func (s *GoalService) MaterializeFrozenLayer(ctx context.Context, parentID string, content DecompositionContent) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		if err := q.LockGoalForWrite(ctx, parentID); err != nil {
			return fmt.Errorf("lock goal for frozen materialize: %w", err)
		}
		parent, err := getGoal(ctx, q, parentID)
		if err != nil {
			return err
		}
		if parent.Kind != KindComposite {
			return ErrInvalidTransition
		}
		if parent.PlannedAt.Valid {
			return nil
		}
		if err := s.validateContent(ctx, parent, content); err != nil {
			return err
		}
		if err := q.SetGoalPlan(ctx, sqlc.SetGoalPlanParams{ID: parent.ID, Plan: marshalJSON(content)}); err != nil {
			return fmt.Errorf("set frozen goal plan: %w", err)
		}
		if err := s.Materialize(ctx, q, parent, content, nil); err != nil {
			return err
		}
		return s.releaseChildren(ctx, q, parent.ID)
	})
}

// ActivateFrozenComposite is idempotent for resume: replay can safely revisit a
// composite that was already activated after its frozen descendants materialized.
func (s *GoalService) ActivateFrozenComposite(ctx context.Context, id string) error {
	cur, err := getGoal(ctx, s.q, id)
	if err != nil {
		return err
	}
	if cur.Kind != KindComposite {
		return ErrInvalidTransition
	}
	if cur.Lifecycle == LifecycleActive {
		return nil
	}
	_, err = s.Activate(ctx, id)
	return err
}
