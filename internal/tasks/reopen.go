package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrReopenConflict is returned when reopening a task without cascade would
// strand downstream tasks in an inconsistent state. The error carries the
// IDs of the conflicting downstream tasks for surfacing in handlers.
type ErrReopenConflict struct {
	DownstreamIDs []string
}

func (e *ErrReopenConflict) Error() string {
	return fmt.Sprintf("reopen would impact %d downstream task(s) without cascade", len(e.DownstreamIDs))
}

// IsConflict reports whether err is an ErrReopenConflict.
func IsConflict(err error) bool {
	var e *ErrReopenConflict
	return errors.As(err, &e)
}

// ReopenTask transitions a done/failed task back to ready. Per D10:
//   - Without cascade: if any reachable downstream task is in a non-terminal
//     non-cancelled state OR done, return ErrReopenConflict listing them.
//   - With cascade=true: reset reachable downstream per ownership:
//     standalone (goal_id IS NULL) -> ready
//     goal-owned under draft/planning goal -> draft
//     goal-owned under any other goal status -> ready
//
// All transitions land in a single tx so partial-cascade failures don't leave
// rows in inconsistent states.
func (s *TransitionService) ReopenTask(ctx context.Context, taskID string, cascade bool, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		switch task.Status {
		case StatusDone, StatusFailed:
			// ok
		case StatusCancelled:
			return fmt.Errorf("cannot reopen cancelled task (D10)")
		default:
			return ErrInvalidTransition
		}
		// Archived tasks are inert; reopening one would resurrect work that is
		// hidden from default lists (D-archive invariant).
		if task.ArchivedAt.Valid {
			return ErrInvalidTransition
		}

		downstream, err := q.ListReachableDownstream(ctx, taskID)
		if err != nil {
			return fmt.Errorf("list downstream: %w", err)
		}

		// Detect conflicts when not cascading.
		if !cascade {
			var conflicts []string
			for _, d := range downstream {
				if d.ArchivedAt.Valid {
					// Archived downstream is hidden and inert; it neither
					// blocks the reopen nor would be reset by a cascade.
					continue
				}
				switch d.Status {
				case StatusCancelled:
					// cancellation is a deliberate stop; doesn't block reopen
				case StatusDraft:
					// drafts are unactivated; ignored
				default:
					conflicts = append(conflicts, d.ID)
				}
			}
			if len(conflicts) > 0 {
				return &ErrReopenConflict{DownstreamIDs: conflicts}
			}
		}

		now := s.now()

		// Transition the reopened task itself.
		if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: StatusReady, UpdatedAt: now, ID: taskID, Status_2: task.Status,
		}); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(taskID),
			EventType:  "reopen",
			FromStatus: nullable(task.Status),
			ToStatus:   nullable(StatusReady),
			ActorType:  actorTypeOrSystem(actor),
			ActorID:    nullable(actor.ID),
			Detail:     detailJSON(map[string]any{"cascade": cascade, "downstream_count": len(downstream)}),
		}); err != nil {
			return err
		}

		if !cascade {
			return nil
		}

		// Cascade: reset each downstream per ownership rule.
		for _, d := range downstream {
			if d.ArchivedAt.Valid {
				// Resetting an archived task would resurrect work that is
				// hidden from default lists (D-archive invariant).
				continue
			}
			target := StatusReady
			if d.GoalID.Valid {
				// Goal-owned: depends on goal status.
				goal, err := q.GetAgentGoal(ctx, d.GoalID.String)
				if err == nil && (goal.Status == "draft" || goal.Status == "planning") {
					target = StatusDraft
				}
			}
			// Clear active run/blocker/review and finalize their owning rows
			// before resetting status; otherwise the reset task would still
			// carry stale active_*_id pointers and leave the underlying
			// run/blocker/review in an open/in-progress state.
			if err := s.clearActiveForReopen(ctx, q, d, now); err != nil {
				return fmt.Errorf("clear active %s: %w", d.ID, err)
			}
			if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
				Status: target, UpdatedAt: now, ID: d.ID, Status_2: d.Status,
			}); err != nil {
				return fmt.Errorf("reset downstream %s: %w", d.ID, err)
			}
			if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
				TaskID:     nullable(d.ID),
				EventType:  "reopen_cascade",
				FromStatus: nullable(d.Status),
				ToStatus:   nullable(target),
				ActorType:  actorTypeOrSystem(actor),
				ActorID:    nullable(actor.ID),
				Detail:     detailJSON(map[string]any{"reopened_from": taskID}),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// clearActiveForReopen finalizes any open run/blocker/review attached to a
// downstream task and clears the matching active_*_id pointer. Called inside
// the reopen tx, before the status transition.
func (s *TransitionService) clearActiveForReopen(ctx context.Context, q *sqlc.Queries, d sqlc.AgentTask, now string) error {
	if d.ActiveRunID.Valid {
		if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
			Status:     RunInterrupted,
			Result:     "",
			Error:      "reopen cascade",
			FinishedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now,
			ID:         d.ActiveRunID.String,
		}); err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
			ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: d.ID,
		}); err != nil {
			return fmt.Errorf("clear active run: %w", err)
		}
	}
	if d.ActiveBlockerID.Valid {
		if _, err := q.CancelAgentTaskBlocker(ctx, sqlc.CancelAgentTaskBlockerParams{
			ResolvedAt: sql.NullString{String: now, Valid: true},
			ID:         d.ActiveBlockerID.String,
		}); err != nil {
			return fmt.Errorf("cancel blocker: %w", err)
		}
		if err := q.SetAgentTaskActiveBlocker(ctx, sqlc.SetAgentTaskActiveBlockerParams{
			ActiveBlockerID: sql.NullString{}, UpdatedAt: now, ID: d.ID,
		}); err != nil {
			return fmt.Errorf("clear active blocker: %w", err)
		}
	}
	if d.ActiveReviewID.Valid {
		if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
			Status:     ReviewCancelled,
			Summary:    "reopen cascade",
			Feedback:   "",
			ResolvedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now,
			ID:         d.ActiveReviewID.String,
		}); err != nil {
			return fmt.Errorf("cancel review: %w", err)
		}
		if err := q.SetAgentTaskActiveReview(ctx, sqlc.SetAgentTaskActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: d.ID,
		}); err != nil {
			return fmt.Errorf("clear active review: %w", err)
		}
	}
	return nil
}
