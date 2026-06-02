package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrGoalNotFound is returned by goal transitions when the row is missing.
var ErrGoalNotFound = errors.New("tasks: goal not found")

// ActivateGoal moves a goal from draft to running and transitions every draft
// child task to ready in the same transaction. The dispatcher picks the
// children up next tick.
func (s *TransitionService) ActivateGoal(ctx context.Context, goalID string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		goal, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if goal.Status != GoalStatusDraft {
			return ErrInvalidTransition
		}
		// Defense in depth against rows that predate goal review gating: a goal
		// whose review_policy needs the unwired synthesizer/review runtime must
		// not activate. CreateGoal already rejects non-none at creation.
		if goal.ReviewPolicy != ReviewPolicyNone {
			return fmt.Errorf("%w: goal review_policy %q (only 'none' is supported)", ErrUnsupportedReviewPolicy, goal.ReviewPolicy)
		}
		now := s.now()
		n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusRunning, UpdatedAt: now, ID: goalID, Status_2: GoalStatusDraft,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		children, err := q.ListChildrenByGoal(ctx, sql.NullString{String: goalID, Valid: true})
		if err != nil {
			return fmt.Errorf("list children: %w", err)
		}
		for _, c := range children {
			if c.Status != StatusDraft {
				continue
			}
			if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
				Status: StatusReady, UpdatedAt: now, ID: c.ID, Status_2: StatusDraft,
			}); err != nil {
				return fmt.Errorf("activate child %s: %w", c.ID, err)
			}
			if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
				TaskID:     nullable(c.ID),
				GoalID:     nullable(goalID),
				EventType:  "activate",
				FromStatus: nullable(StatusDraft),
				ToStatus:   nullable(StatusReady),
				ActorType:  actorTypeOrSystem(actor),
				ActorID:    nullable(actor.ID),
				Detail:     detailJSON(map[string]any{"goal_activation": true}),
			}); err != nil {
				return err
			}
		}
		return s.appendGoalEvent(ctx, q, goalID, "goal_activate", GoalStatusDraft, GoalStatusRunning, actor, nil)
	})
}

// CompleteGoal moves a non-terminal goal to done and stamps completed_at.
// Used by goal rollup (all required children done + review policy = none) and
// by review-decision approval.
func (s *TransitionService) CompleteGoal(ctx context.Context, goalID, output string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		goal, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if isTerminalGoalStatus(goal.Status) {
			return ErrInvalidTransition
		}
		now := s.now()
		n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusDone, UpdatedAt: now, ID: goalID, Status_2: goal.Status,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		if output == "" {
			output = goal.Output
		}
		if err := q.SetAgentGoalOutput(ctx, sqlc.SetAgentGoalOutputParams{
			Output: output, CompletedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		return s.appendGoalEvent(ctx, q, goalID, "goal_complete", goal.Status, GoalStatusDone, actor, nil)
	})
}

// FailGoal marks a non-terminal goal as failed. Active review is cleared.
func (s *TransitionService) FailGoal(ctx context.Context, goalID, reason string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		goal, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if isTerminalGoalStatus(goal.Status) {
			return ErrInvalidTransition
		}
		now := s.now()
		n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusFailed, UpdatedAt: now, ID: goalID, Status_2: goal.Status,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		return s.appendGoalEvent(ctx, q, goalID, "goal_fail", goal.Status, GoalStatusFailed, actor,
			map[string]any{"reason": reason})
	})
}

// CancelGoal cancels a non-terminal goal and cascade-cancels every non-terminal
// child task. Active runs / blockers / reviews on each child are finalized so
// the database invariants (active_*_id consistency) hold post-cancel.
func (s *TransitionService) CancelGoal(ctx context.Context, goalID, reason string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		goal, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if isTerminalGoalStatus(goal.Status) {
			return ErrInvalidTransition
		}
		now := s.now()
		children, err := q.ListChildrenByGoal(ctx, sql.NullString{String: goalID, Valid: true})
		if err != nil {
			return fmt.Errorf("list children: %w", err)
		}
		for _, c := range children {
			if IsTerminalStatus(c.Status) {
				continue
			}
			// Reuse the reopen-cascade cleanup: it interrupts the active run,
			// cancels the active blocker, cancels the active review, and
			// clears the pointers. Then transition to cancelled.
			if err := s.clearActiveForReopen(ctx, q, c, now); err != nil {
				return fmt.Errorf("clear active %s: %w", c.ID, err)
			}
			if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
				Status: StatusCancelled, UpdatedAt: now, ID: c.ID, Status_2: c.Status,
			}); err != nil {
				return fmt.Errorf("cancel child %s: %w", c.ID, err)
			}
			if err := q.SetAgentTaskCancelled(ctx, sqlc.SetAgentTaskCancelledParams{
				CancelledAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: c.ID,
			}); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
				TaskID:     nullable(c.ID),
				GoalID:     nullable(goalID),
				EventType:  "cancel",
				FromStatus: nullable(c.Status),
				ToStatus:   nullable(StatusCancelled),
				ActorType:  actorTypeOrSystem(actor),
				ActorID:    nullable(actor.ID),
				Detail:     detailJSON(map[string]any{"goal_cancel": true}),
			}); err != nil {
				return err
			}
		}
		n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusCancelled, UpdatedAt: now, ID: goalID, Status_2: goal.Status,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		return s.appendGoalEvent(ctx, q, goalID, "goal_cancel", goal.Status, GoalStatusCancelled, actor,
			map[string]any{"reason": reason})
	})
}

// BlockGoal mirrors a child's blocked state on the goal. Used by rollup when a
// required child is blocked. No goal-level blocker row is created (D2 keeps
// the goal as a thin container); the audit event records the cause.
func (s *TransitionService) BlockGoal(ctx context.Context, goalID, reason string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		goal, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if goal.Status != GoalStatusRunning {
			return ErrInvalidTransition
		}
		now := s.now()
		n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusBlocked, UpdatedAt: now, ID: goalID, Status_2: GoalStatusRunning,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		return s.appendGoalEvent(ctx, q, goalID, "goal_block", GoalStatusRunning, GoalStatusBlocked, actor,
			map[string]any{"reason": reason})
	})
}

// UnblockGoal recovers a goal from blocked back to running once no required
// child is blocked or failed. Mirror of BlockGoal; driven by rollup when a
// child blocker is resolved or the failed dependency is waived. The goal then
// resumes normal rollup (completing or re-blocking) on subsequent ticks.
func (s *TransitionService) UnblockGoal(ctx context.Context, goalID, reason string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		goal, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if goal.Status != GoalStatusBlocked {
			return ErrInvalidTransition
		}
		now := s.now()
		n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusRunning, UpdatedAt: now, ID: goalID, Status_2: GoalStatusBlocked,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		return s.appendGoalEvent(ctx, q, goalID, "goal_unblock", GoalStatusBlocked, GoalStatusRunning, actor,
			map[string]any{"reason": reason})
	})
}

// CompleteGoalTx runs CompleteGoal's body in an existing transaction. Used
// by the dispatcher rollup loop, which holds the tx open across multiple
// goals.
func (s *TransitionService) CompleteGoalTx(ctx context.Context, q *sqlc.Queries, goalID string, actor Actor) error {
	now := s.now()
	goal, err := getGoalForUpdate(ctx, q, goalID)
	if err != nil {
		return err
	}
	if isTerminalGoalStatus(goal.Status) {
		return ErrInvalidTransition
	}
	n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
		Status: GoalStatusDone, UpdatedAt: now, ID: goalID, Status_2: goal.Status,
	})
	if err != nil || n == 0 {
		if err == nil {
			err = ErrInvalidTransition
		}
		return err
	}
	if err := q.SetAgentGoalOutput(ctx, sqlc.SetAgentGoalOutputParams{
		Output: goal.Output, CompletedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: goalID,
	}); err != nil {
		return err
	}
	return s.appendGoalEvent(ctx, q, goalID, "goal_complete", goal.Status, GoalStatusDone, actor, nil)
}

// appendGoalEvent is a thin convenience for goal-only events.
func (s *TransitionService) appendGoalEvent(ctx context.Context, q *sqlc.Queries, goalID, eventType, from, to string, actor Actor, detail map[string]any) error {
	return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		GoalID:     nullable(goalID),
		EventType:  eventType,
		FromStatus: nullable(from),
		ToStatus:   nullable(to),
		ActorType:  actorTypeOrSystem(actor),
		ActorID:    nullable(actor.ID),
		Detail:     detailJSON(detail),
	})
}

// getGoalForUpdate fetches a goal row by id with the not-found error mapped.
func getGoalForUpdate(ctx context.Context, q *sqlc.Queries, goalID string) (sqlc.AgentGoal, error) {
	g, err := q.GetAgentGoal(ctx, goalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.AgentGoal{}, ErrGoalNotFound
		}
		return sqlc.AgentGoal{}, err
	}
	return g, nil
}

// isTerminalGoalStatus reports whether a goal status forbids further
// transitions.
func isTerminalGoalStatus(s string) bool {
	switch s {
	case GoalStatusDone, GoalStatusFailed, GoalStatusCancelled:
		return true
	}
	return false
}
