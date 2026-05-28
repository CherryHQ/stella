package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Review-side typed errors. Callers branch on these.
var (
	ErrReviewNotFound = errors.New("tasks: review not found")
	ErrReviewClosed   = errors.New("tasks: review already resolved")
)

// Review status constants.
const (
	ReviewRequested        = "requested"
	ReviewInProgress       = "in_progress"
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewRejected         = "rejected"
	ReviewEscalated        = "escalated"
	ReviewCancelled        = "cancelled"
)

// Review policy constants.
const (
	ReviewPolicyNone  = "none"
	ReviewPolicyAuto  = "auto"
	ReviewPolicyAgent = "agent"
	ReviewPolicyHuman = "human"
)

// Reviewer type constants (mirrors enum in schema).
const (
	ReviewerSystem = "system"
	ReviewerAgent  = "agent"
	ReviewerHuman  = "human"
)

// Slice 2 widens task status with this value.
const StatusReviewing = "reviewing"

// SubmitWithReview is the Slice 2 extension of Submit. Routes per the task's
// review_policy:
//
//   - none:  task -> done immediately (no review row, no event), as Slice 1
//   - auto:  insert a system-approved agent_review for audit, task -> done
//   - agent: insert a 'requested' agent_review (reviewer_type=agent),
//     task -> reviewing; dispatcher picks up reviewer run
//   - human: insert a 'requested' agent_review (reviewer_type=human),
//     task -> reviewing; awaits human decision via API
//
// Callers (control_tool.Submit / worker) invoke this when the worker says
// "submit". Phase 9 of the plan.
func (s *TransitionService) SubmitWithReview(ctx context.Context, taskID, runID, output string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := expect(ctx, q, taskID, StatusRunning)
		if err != nil {
			return err
		}
		now := s.now()
		policy := task.ReviewPolicy

		// 'none' path: short-circuit to done. Same as Slice 1's Submit().
		if policy == ReviewPolicyNone {
			return s.completeTaskInline(ctx, q, taskID, runID, output, actor, now)
		}

		// 'auto' path: write a system-approved review for audit, then done.
		if policy == ReviewPolicyAuto {
			reviewID, err := insertReview(ctx, q, taskID, runID, ReviewerSystem, "", now)
			if err != nil {
				return err
			}
			if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
				Status: ReviewApproved, Summary: "auto-approved", Feedback: "",
				ResolvedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:  now, ID: reviewID,
			}); err != nil {
				return err
			}
			return s.completeTaskInline(ctx, q, taskID, runID, output, actor, now)
		}

		// 'agent' or 'human': open review + task -> reviewing.
		reviewerType := ReviewerHuman
		if policy == ReviewPolicyAgent {
			reviewerType = ReviewerAgent
		}
		reviewID, err := insertReview(ctx, q, taskID, runID, reviewerType, "", now)
		if err != nil {
			return err
		}
		if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: StatusReviewing, UpdatedAt: now, ID: taskID, Status_2: StatusRunning,
		}); err != nil {
			return err
		}
		if err := q.SetAgentTaskActiveReview(ctx, sqlc.SetAgentTaskActiveReviewParams{
			ActiveReviewID: nullable(reviewID), UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		// Finalize the worker run; output goes onto the task so the reviewer
		// has something to review.
		if runID != "" {
			if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: RunCompleted, Result: output, Error: "",
				FinishedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:  now, ID: runID,
			}); err != nil {
				return err
			}
		}
		if output != "" {
			if err := q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
				Output: output, CompletedAt: sql.NullString{}, UpdatedAt: now, ID: taskID,
			}); err != nil {
				return err
			}
		}
		if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
			ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(taskID),
			RunID:      nullable(runID),
			ReviewID:   nullable(reviewID),
			EventType:  "submit_for_review",
			FromStatus: nullable(StatusRunning),
			ToStatus:   nullable(StatusReviewing),
			ActorType:  actorTypeOrSystem(actor),
			ActorID:    nullable(actor.ID),
		})
	})
}

// completeTaskInline mirrors Slice 1's Submit body so the policy=none path is
// identical. Kept inline (not refactored back into Submit) to keep the Phase 9
// review pipeline visibly separable.
func (s *TransitionService) completeTaskInline(ctx context.Context, q *sqlc.Queries, taskID, runID, output string, actor Actor, now string) error {
	if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
		Status: StatusDone, UpdatedAt: now, ID: taskID, Status_2: StatusRunning,
	}); err != nil {
		return err
	}
	if output != "" {
		if err := q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
			Output: output, CompletedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
	}
	if runID != "" {
		if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
			Status: RunCompleted, Result: output, Error: "",
			FinishedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now, ID: runID,
		}); err != nil {
			return err
		}
	}
	if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
		ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: taskID,
	}); err != nil {
		return err
	}
	return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		TaskID: nullable(taskID), RunID: nullable(runID),
		EventType: "submit", FromStatus: nullable(StatusRunning), ToStatus: nullable(StatusDone),
		ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
	})
}

// ApproveReview closes a review with 'approved' and moves the task to done.
func (s *TransitionService) ApproveReview(ctx context.Context, reviewID, summary string, actor Actor) error {
	return s.decideReview(ctx, reviewID, ReviewApproved, summary, "", StatusDone, actor)
}

// RejectReview closes a review with 'rejected' and moves the task to failed.
func (s *TransitionService) RejectReview(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return s.decideReview(ctx, reviewID, ReviewRejected, summary, feedback, StatusFailed, actor)
}

// RequestChanges closes a review with 'changes_requested'. If the task has
// retry budget, it returns to 'ready'; otherwise it transitions to 'failed'.
func (s *TransitionService) RequestChanges(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		review, err := q.GetAgentReview(ctx, reviewID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrReviewNotFound
			}
			return err
		}
		if review.Status != ReviewRequested && review.Status != ReviewInProgress {
			return ErrReviewClosed
		}
		if !review.TaskID.Valid {
			return fmt.Errorf("RequestChanges: review %s has no task", reviewID)
		}
		taskID := review.TaskID.String
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
			Status: ReviewChangesRequested, Summary: summary, Feedback: feedback,
			ResolvedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now, ID: reviewID,
		}); err != nil {
			return err
		}
		nextStatus := StatusReady
		if task.RetryCount+1 > task.MaxRetries {
			nextStatus = StatusFailed
		} else if err := q.IncrementAgentTaskRetry(ctx, sqlc.IncrementAgentTaskRetryParams{UpdatedAt: now, ID: taskID}); err != nil {
			return err
		}
		if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: nextStatus, UpdatedAt: now, ID: taskID, Status_2: StatusReviewing,
		}); err != nil {
			return err
		}
		if err := q.SetAgentTaskActiveReview(ctx, sqlc.SetAgentTaskActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID: nullable(taskID), ReviewID: nullable(reviewID),
			EventType:  "review_changes_requested",
			FromStatus: nullable(StatusReviewing), ToStatus: nullable(nextStatus),
			ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
			Detail: detailJSON(map[string]any{"summary": summary, "feedback": feedback}),
		})
	})
}

// EscalateReview closes an agent review with 'escalated' and inserts a fresh
// review row with reviewer_type='human' + escalated_from_review_id pointing
// back. Task stays in 'reviewing' under the new active_review_id (D8).
func (s *TransitionService) EscalateReview(ctx context.Context, reviewID, reason string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		review, err := q.GetAgentReview(ctx, reviewID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrReviewNotFound
			}
			return err
		}
		if review.Status != ReviewRequested && review.Status != ReviewInProgress {
			return ErrReviewClosed
		}
		if review.ReviewerType != ReviewerAgent {
			return fmt.Errorf("EscalateReview: only agent reviews can escalate (got %q)", review.ReviewerType)
		}
		now := s.now()
		if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
			Status: ReviewEscalated, Summary: reason, Feedback: "",
			ResolvedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now, ID: reviewID,
		}); err != nil {
			return err
		}
		// Create new human review chained back.
		newID := uuid.NewString()
		if _, err := q.CreateAgentReview(ctx, sqlc.CreateAgentReviewParams{
			ID:                    newID,
			TaskID:                review.TaskID,
			GoalID:                review.GoalID,
			SubmittedRunID:        review.SubmittedRunID,
			ReviewerRunID:         sql.NullString{},
			ReviewerType:          ReviewerHuman,
			ReviewerUserID:        sql.NullString{},
			EscalatedFromReviewID: nullable(reviewID),
			Status:                ReviewRequested,
			Summary:               "",
			Feedback:              "",
			CreatedAt:             now,
			UpdatedAt:             now,
		}); err != nil {
			return err
		}
		if review.TaskID.Valid {
			if err := q.SetAgentTaskActiveReview(ctx, sqlc.SetAgentTaskActiveReviewParams{
				ActiveReviewID: nullable(newID), UpdatedAt: now, ID: review.TaskID.String,
			}); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID: review.TaskID, ReviewID: nullable(reviewID),
			EventType: "review_escalated",
			ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
			Detail: detailJSON(map[string]any{"escalated_to_review_id": newID, "reason": reason}),
		})
	})
}

// decideReview is the shared path for approve / reject — both terminal-status
// decisions that route the task to a fixed next state.
func (s *TransitionService) decideReview(ctx context.Context, reviewID, decision, summary, feedback, nextTaskStatus string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		review, err := q.GetAgentReview(ctx, reviewID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrReviewNotFound
			}
			return err
		}
		if review.Status != ReviewRequested && review.Status != ReviewInProgress {
			return ErrReviewClosed
		}
		if !review.TaskID.Valid {
			return fmt.Errorf("decideReview: review %s has no task parent", reviewID)
		}
		taskID := review.TaskID.String
		now := s.now()
		if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
			Status: decision, Summary: summary, Feedback: feedback,
			ResolvedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now, ID: reviewID,
		}); err != nil {
			return err
		}
		if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: nextTaskStatus, UpdatedAt: now, ID: taskID, Status_2: StatusReviewing,
		}); err != nil {
			return err
		}
		if nextTaskStatus == StatusDone {
			_ = q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
				Output: "", CompletedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: taskID,
			})
		}
		if err := q.SetAgentTaskActiveReview(ctx, sqlc.SetAgentTaskActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID: nullable(taskID), ReviewID: nullable(reviewID),
			EventType:  "review_" + decision,
			FromStatus: nullable(StatusReviewing), ToStatus: nullable(nextTaskStatus),
			ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
			Detail: detailJSON(map[string]any{"summary": summary, "feedback": feedback}),
		})
	})
}

// insertReview is the shared helper used by SubmitWithReview's branches.
func insertReview(ctx context.Context, q *sqlc.Queries, taskID, submittedRunID, reviewerType, summary, now string) (string, error) {
	id := uuid.NewString()
	_, err := q.CreateAgentReview(ctx, sqlc.CreateAgentReviewParams{
		ID:                    id,
		TaskID:                nullable(taskID),
		GoalID:                sql.NullString{},
		SubmittedRunID:        nullable(submittedRunID),
		ReviewerRunID:         sql.NullString{},
		ReviewerType:          reviewerType,
		ReviewerUserID:        sql.NullString{},
		EscalatedFromReviewID: sql.NullString{},
		Status:                ReviewRequested,
		Summary:               summary,
		Feedback:              "",
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	return id, err
}
