package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Review-side typed errors. Callers branch on these.
var (
	ErrReviewNotFound = errors.New("tasks: review not found")
	ErrReviewClosed   = errors.New("tasks: review already resolved")
	// ErrUnsupportedReviewPolicy marks a goal review_policy that no runtime in
	// this build can service. Goals only support 'none'; goal-level review
	// (auto/agent/human) needs the unwired synthesizer/goal-review runtime.
	// The API maps this to a 400 validation error.
	ErrUnsupportedReviewPolicy = errors.New("tasks: unsupported review_policy")
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

// validReviewPolicy reports whether p is a known review policy.
func validReviewPolicy(p string) bool {
	switch p {
	case ReviewPolicyNone, ReviewPolicyAuto, ReviewPolicyAgent, ReviewPolicyHuman:
		return true
	default:
		return false
	}
}

// Reviewer type constants (mirrors enum in schema).
const (
	ReviewerSystem = "system"
	ReviewerAgent  = "agent"
	ReviewerHuman  = "human"
)

// Slice 2 widens task status with this value.
const StatusReviewing = "reviewing"

// Submit handles a worker's submit action, routing on the task's review_policy:
//
//   - none:  task -> done immediately (no review row, no event)
//   - auto:  insert a system-approved agent_review for audit, task -> done
//   - agent: insert a 'requested' agent_review (reviewer_type=agent),
//     task -> reviewing; dispatcher picks up reviewer run
//   - human: insert a 'requested' agent_review (reviewer_type=human),
//     task -> reviewing; awaits human decision via API
//
// Single source of truth for submit semantics; worker / control tool / API
// callers all go through here.
func (s *TransitionService) Submit(ctx context.Context, taskID, runID, output string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := expect(ctx, q, taskID, StatusRunning)
		if err != nil {
			return err
		}
		if !runIdentityMatches(task, runID) {
			return ErrInvalidTransition
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
				ResolvedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt:  now, ID: reviewID,
			}); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
				TaskID: nullable(taskID), RunID: nullable(runID), ReviewID: nullable(reviewID),
				EventType: "review_approved",
				ActorType: ActorSystem,
				Detail:    detailJSON(map[string]any{"auto": true, "summary": "auto-approved"}),
			}); err != nil {
				return err
			}
			return s.completeTaskInline(ctx, q, taskID, runID, output, actor, now)
		}

		// 'agent' or 'human': open review + task -> reviewing. The agent
		// reviewer runtime is not auto-dispatched in this build (no scan path),
		// but agent-review rows remain resolvable via the human review API
		// (approve/reject/escalate), so the row is created here as before.
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
				FinishedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt:  now, ID: runID,
			}); err != nil {
				return err
			}
		}
		if output != "" {
			if err := q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
				Output: output, CompletedAt: sql.NullTime{}, UpdatedAt: now, ID: taskID,
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
func (s *TransitionService) completeTaskInline(ctx context.Context, q *sqlc.Queries, taskID, runID, output string, actor Actor, now time.Time) error {
	if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
		Status: StatusDone, UpdatedAt: now, ID: taskID, Status_2: StatusRunning,
	}); err != nil {
		return err
	}
	if output != "" {
		if err := q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
			Output: output, CompletedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
	}
	if runID != "" {
		if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
			Status: RunCompleted, Result: output, Error: "",
			FinishedAt: sql.NullTime{Time: now, Valid: true},
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

// ApproveReview closes a review with 'approved'. The caller doesn't need to
// know whether the parent is a task or a goal; this dispatcher branches on
// the row's parent column and routes to the right inner transition.
func (s *TransitionService) ApproveReview(ctx context.Context, reviewID, summary string, actor Actor) error {
	return s.decideAnyReview(ctx, reviewID, ReviewApproved, summary, "", actor)
}

// RejectReview closes a review with 'rejected'. Task parent → task failed,
// goal parent → goal failed.
func (s *TransitionService) RejectReview(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return s.decideAnyReview(ctx, reviewID, ReviewRejected, summary, feedback, actor)
}

// RequestChanges closes a review with 'changes_requested'. On a task review
// the task either bounces back to 'ready' (with retry budget consumed) or
// transitions to 'failed'. On a goal review there is no retry budget — the
// goal goes to 'failed' with a documented gap (no synthesizer retry yet).
func (s *TransitionService) RequestChanges(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return s.decideAnyReview(ctx, reviewID, ReviewChangesRequested, summary, feedback, actor)
}

// decideAnyReview is the shared dispatcher for the three external decision
// entry points. It loads the review once, validates it's still open, then
// routes by parent type.
func (s *TransitionService) decideAnyReview(ctx context.Context, reviewID, decision, summary, feedback string, actor Actor) error {
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
		if review.TaskID.Valid {
			return s.decideTaskReviewInTx(ctx, q, review, decision, summary, feedback, actor)
		}
		if review.GoalID.Valid {
			return s.decideGoalReviewInTx(ctx, q, review, decision, summary, feedback, actor)
		}
		return fmt.Errorf("decideAnyReview: review %s has neither task nor goal parent", reviewID)
	})
}

// decideTaskReviewInTx handles the three decisions for a task-parented
// review.
func (s *TransitionService) decideTaskReviewInTx(ctx context.Context, q *sqlc.Queries, review sqlc.AgentReview, decision, summary, feedback string, actor Actor) error {
	taskID := review.TaskID.String
	task, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
		Status: decision, Summary: summary, Feedback: feedback,
		ResolvedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt:  now, ID: review.ID,
	}); err != nil {
		return err
	}

	var nextStatus string
	switch decision {
	case ReviewApproved:
		nextStatus = StatusDone
	case ReviewRejected:
		nextStatus = StatusFailed
	case ReviewChangesRequested:
		nextStatus = StatusReady
		if task.RetryCount+1 > task.MaxRetries {
			nextStatus = StatusFailed
		} else if err := q.IncrementAgentTaskRetry(ctx, sqlc.IncrementAgentTaskRetryParams{UpdatedAt: now, ID: taskID}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("decideTaskReviewInTx: unknown decision %q", decision)
	}

	if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
		Status: nextStatus, UpdatedAt: now, ID: taskID, Status_2: StatusReviewing,
	}); err != nil {
		return err
	}
	if nextStatus == StatusDone {
		_ = q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
			Output: "", CompletedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: taskID,
		})
	}
	if err := q.SetAgentTaskActiveReview(ctx, sqlc.SetAgentTaskActiveReviewParams{
		ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: taskID,
	}); err != nil {
		return err
	}
	return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		TaskID: nullable(taskID), ReviewID: nullable(review.ID),
		EventType:  reviewEventType(decision),
		FromStatus: nullable(StatusReviewing), ToStatus: nullable(nextStatus),
		ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
		Detail: detailJSON(map[string]any{"summary": summary, "feedback": feedback}),
	})
}

// decideGoalReviewInTx handles the three decisions for a goal-parented
// review.
//
// Approve → goal.done; reject → goal.failed; request_changes → goal.failed
// (documented gap: goal-side synthesizer retry budget not yet modelled).
func (s *TransitionService) decideGoalReviewInTx(ctx context.Context, q *sqlc.Queries, review sqlc.AgentReview, decision, summary, feedback string, actor Actor) error {
	goalID := review.GoalID.String
	goal, err := q.GetAgentGoal(ctx, goalID)
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
		Status: decision, Summary: summary, Feedback: feedback,
		ResolvedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt:  now, ID: review.ID,
	}); err != nil {
		return err
	}

	var nextStatus string
	switch decision {
	case ReviewApproved:
		nextStatus = GoalStatusDone
	case ReviewRejected, ReviewChangesRequested:
		nextStatus = GoalStatusFailed
	default:
		return fmt.Errorf("decideGoalReviewInTx: unknown decision %q", decision)
	}

	n, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
		Status: nextStatus, UpdatedAt: now, ID: goalID, Status_2: goal.Status,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidTransition
	}
	if nextStatus == GoalStatusDone {
		if err := q.SetAgentGoalOutput(ctx, sqlc.SetAgentGoalOutputParams{
			Output: goal.Output, CompletedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
	}
	if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
		ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: goalID,
	}); err != nil {
		return err
	}
	return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		GoalID: nullable(goalID), ReviewID: nullable(review.ID),
		EventType:  reviewEventType(decision),
		FromStatus: nullable(goal.Status), ToStatus: nullable(nextStatus),
		ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
		Detail: detailJSON(map[string]any{"summary": summary, "feedback": feedback}),
	})
}

// reviewEventType maps a decision value to its event_type label.
func reviewEventType(decision string) string {
	switch decision {
	case ReviewChangesRequested:
		return "review_changes_requested"
	default:
		return "review_" + decision
	}
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
			ResolvedAt: sql.NullTime{Time: now, Valid: true},
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
		if review.GoalID.Valid {
			if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
				ActiveReviewID: nullable(newID), UpdatedAt: now, ID: review.GoalID.String,
			}); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID: review.TaskID, GoalID: review.GoalID, ReviewID: nullable(reviewID),
			EventType: "review_escalated",
			ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
			Detail: detailJSON(map[string]any{"escalated_to_review_id": newID, "reason": reason}),
		})
	})
}

// insertReview is the shared helper used by Submit's review branches.
func insertReview(ctx context.Context, q *sqlc.Queries, taskID, submittedRunID, reviewerType, summary string, now time.Time) (string, error) {
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
