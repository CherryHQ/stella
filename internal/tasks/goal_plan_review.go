package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// goal_plan_review.go is the structured-plan lifecycle (#525 Phase 5): stage a
// plan as a pending edit, accept it directly (review_policy none) or route it
// through a dedicated human plan review, then materialize. Plan reviews are a
// separate lifecycle from goal-completion reviews — they accept a plan, never
// mark a goal done — so they carry subject='plan' and the generic review API
// (ApproveReview/RejectReview/RequestChanges) refuses them (D8a / opus B1).

// CreateGoalPlan stages a structured plan as the goal's single pending edit
// (create-or-replace; goal_id is UNIQUE so this is an upsert, never a 2nd row,
// codex BLOCKER 4). It refuses to clobber a plan that is currently under review
// (2nd-pass B3) — request changes or cancel that review first. The goal moves
// draft -> planning; content_json (last materialized) is untouched. The edit is
// realized later by AcceptGoalPlan (review_policy none) or the review path, then
// MaterializeGoalPlan.
func (f *ServiceFacade) CreateGoalPlan(ctx context.Context, goalID string, content PlanContent, reviewPolicy string, actor Actor) error {
	if reviewPolicy == "" {
		reviewPolicy = ReviewPolicyNone
	}
	if reviewPolicy != ReviewPolicyNone && reviewPolicy != ReviewPolicyHuman {
		return fmt.Errorf("%w: plan review_policy %q (only none|human)", ErrUnsupportedReviewPolicy, reviewPolicy)
	}
	if err := validatePlan(content); err != nil {
		return err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	goal, err := f.q.GetAgentGoal(ctx, goalID)
	if err != nil {
		return err
	}
	if isTerminalGoalStatus(goal.Status) {
		return fmt.Errorf("%w: goal is %s", ErrInvalidTransition, goal.Status)
	}
	now := f.svc.now()
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		existing, err := q.GetAgentGoalPlanByGoal(ctx, goalID)
		switch {
		case err == nil && existing.Status == PlanStatusInReview:
			return ErrPlanReviewExists
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := q.UpsertAgentGoalPlanPending(ctx, sqlc.UpsertAgentGoalPlanPendingParams{
			ID:                 uuid.NewString(),
			GoalID:             goalID,
			Status:             PlanStatusDraft,
			ReviewPolicy:       reviewPolicy,
			PendingContentJson: nullable(string(raw)),
		}); err != nil {
			return fmt.Errorf("upsert plan: %w", err)
		}
		if _, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusPlanning, UpdatedAt: now, ID: goalID, Status_2: GoalStatusDraft,
		}); err != nil {
			return fmt.Errorf("promote goal planning: %w", err)
		}
		return f.svc.appendGoalEvent(ctx, q, goalID, "goal_plan_created", goal.Status, planningIfDraft(goal.Status), actor,
			map[string]any{"review_policy": reviewPolicy})
	})
}

// AcceptGoalPlan accepts a goal's pending plan without review (review_policy
// none): validate, stamp accepted_at + status accepted. It does NOT promote the
// goal to planned — that happens only in MaterializeGoalPlan (2nd-pass B1) — so
// the goal stays planning. A plan whose review_policy is human must go through
// SubmitGoalPlanForReview instead.
func (f *ServiceFacade) AcceptGoalPlan(ctx context.Context, goalID string, actor Actor) error {
	now := f.svc.now()
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		plan, err := loadGoalPlan(ctx, q, goalID)
		if err != nil {
			return err
		}
		if plan.ReviewPolicy != ReviewPolicyNone {
			return fmt.Errorf("AcceptGoalPlan: plan review_policy is %q, use SubmitGoalPlanForReview", plan.ReviewPolicy)
		}
		content, err := parsePlanContent(pendingOrContent(plan))
		if err != nil {
			return err
		}
		if err := validatePlan(content); err != nil {
			return err
		}
		return q.SetAgentGoalPlanAccepted(ctx, sqlc.SetAgentGoalPlanAcceptedParams{
			Status: PlanStatusAccepted, AcceptedAt: nullable(now), ID: plan.ID,
		})
	})
}

// MaterializeGoalPlan realizes an accepted/approved plan: it pre-mints a session
// per pending item outside the tx (SQLite single-writer, SF1; matched items
// ignore theirs) then runs the generic reconcile in one tx, so a partial
// materialize never escapes (2nd-pass B1).
func (f *ServiceFacade) MaterializeGoalPlan(ctx context.Context, goalID string, actor Actor) error {
	if f.newSession == nil {
		return fmt.Errorf("%w: task session minter is not configured", ErrInvalidTaskContext)
	}
	goal, err := f.q.GetAgentGoal(ctx, goalID)
	if err != nil {
		return err
	}
	plan, err := loadGoalPlan(ctx, f.q, goalID)
	if err != nil {
		return err
	}
	// Prove the plan is materializable before minting any session, so an invalid /
	// draft / in_review plan can't leak orphan sessions (the tx revalidates too).
	if plan.Status != PlanStatusAccepted && plan.Status != PlanStatusApproved {
		return fmt.Errorf("%w: plan status %q", ErrAcceptedPlanRequired, plan.Status)
	}
	rawContent := pendingOrContent(plan)
	content, err := parsePlanContent(rawContent)
	if err != nil {
		return err
	}
	sessions := make(map[string]string, len(content.Items))
	for _, it := range content.Items {
		sid, err := f.newSession(ctx, goal.UserID, goal.AgentID, goal.ProjectID.String)
		if err != nil {
			return fmt.Errorf("mint plan-task session: %w", err)
		}
		sessions[it.ID] = sid
	}
	now := f.svc.now()
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		// Reload inside the tx and abort if the plan moved under us: the sessions
		// were minted for rawContent, the reconcile builds the graph from fresh, and
		// PromoteAgentGoalPlanMaterialized promotes whatever pending is in the DB now.
		// Pinning all three to the same reloaded row keeps the graph and the promoted
		// content_json from diverging under a concurrent CreateGoalPlan (codex BLOCKER 1).
		fresh, err := loadGoalPlan(ctx, q, goalID)
		if err != nil {
			return err
		}
		if pendingOrContent(fresh) != rawContent {
			return ErrPlanChangedDuringMaterialize
		}
		freshGoal, err := f.q.GetAgentGoal(ctx, goalID)
		if err != nil {
			return err
		}
		return f.materializeGoalPlanInTx(ctx, q, freshGoal, fresh, sessions, now)
	})
}

// SubmitGoalPlanForReview opens a human review of the goal's pending plan edit
// (requires review_policy=human and a pending edit). The plan moves draft ->
// in_review and the goal gets an active plan review. The goal status moves draft
// -> planning for a first plan; a replan on a planned/running goal stays where it
// is, so the already-materialized work keeps running while the edit is reviewed
// (BLOCKER 1). Returns the new review id.
func (f *ServiceFacade) SubmitGoalPlanForReview(ctx context.Context, goalID string, actor Actor) (string, error) {
	now := f.svc.now()
	var reviewID string
	err := f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		goal, err := f.q.GetAgentGoal(ctx, goalID)
		if err != nil {
			return err
		}
		if isTerminalGoalStatus(goal.Status) {
			return fmt.Errorf("%w: goal is %s", ErrInvalidTransition, goal.Status)
		}
		plan, err := loadGoalPlan(ctx, q, goalID)
		if err != nil {
			return err
		}
		if plan.ReviewPolicy != ReviewPolicyHuman {
			return fmt.Errorf("SubmitGoalPlanForReview: plan review_policy is %q, not human", plan.ReviewPolicy)
		}
		if !plan.PendingContentJson.Valid {
			return ErrNoPendingPlanEdit
		}
		if _, err := q.GetOpenReviewForGoalSubject(ctx, sqlc.GetOpenReviewForGoalSubjectParams{
			GoalID: nullable(goalID), Subject: ReviewSubjectPlan,
		}); err == nil {
			return ErrPlanReviewExists
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// draft -> in_review; 0 rows means it was already in_review (raced).
		n, err := q.SetAgentGoalPlanInReview(ctx, sqlc.SetAgentGoalPlanInReviewParams{
			Status: PlanStatusInReview, ID: plan.ID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrPlanReviewExists
		}
		reviewID = uuid.NewString()
		if _, err := q.CreateAgentReview(ctx, sqlc.CreateAgentReviewParams{
			ID:           reviewID,
			TaskID:       sql.NullString{},
			GoalID:       nullable(goalID),
			ReviewerType: ReviewerHuman,
			Status:       ReviewRequested,
			Subject:      ReviewSubjectPlan,
			CreatedAt:    now,
			UpdatedAt:    now,
		}); err != nil {
			return err
		}
		// agent_goal.active_review_id is a single scalar pointer. A plan review and a
		// goal-completion review can coexist in agent_review (the open-review index is
		// subject-aware), but only one can be the goal's "active" pointer. That is safe
		// today because goal-completion review_policy is none-only (ActivateGoal rejects
		// others), so no completion review ever competes for the pointer. If completion
		// reviews on goals become real, this needs a subject-keyed pointer (codex P2).
		if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
			ActiveReviewID: nullable(reviewID), UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		if _, err := q.TransitionAgentGoalStatus(ctx, sqlc.TransitionAgentGoalStatusParams{
			Status: GoalStatusPlanning, UpdatedAt: now, ID: goalID, Status_2: GoalStatusDraft,
		}); err != nil {
			return err
		}
		return f.svc.appendGoalEvent(ctx, q, goalID, "goal_plan_submitted_for_review", goal.Status, planningIfDraft(goal.Status), actor,
			map[string]any{"review_id": reviewID})
	})
	return reviewID, err
}

// ApproveGoalPlanReview closes a plan review as approved: the plan becomes
// approved + records the deciding review + accepted_at, ready for the next
// MaterializeGoalPlan to promote. It does NOT promote the plan content or the
// goal to planned (that is materialize's job, 2nd-pass B1), and never marks the
// goal done. The goal status is left untouched (planning for a first plan;
// planned/running for a replan).
func (f *ServiceFacade) ApproveGoalPlanReview(ctx context.Context, reviewID, summary string, actor Actor) error {
	now := f.svc.now()
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		review, err := loadOpenPlanReview(ctx, q, reviewID)
		if err != nil {
			return err
		}
		goalID := review.GoalID.String
		plan, err := loadGoalPlan(ctx, q, goalID)
		if err != nil {
			return err
		}
		if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
			Status: ReviewApproved, Summary: summary, Feedback: "",
			ResolvedAt: nullable(now), UpdatedAt: now, ID: reviewID,
		}); err != nil {
			return err
		}
		if err := q.SetAgentGoalPlanApproved(ctx, sqlc.SetAgentGoalPlanApprovedParams{
			Status: PlanStatusApproved, ApprovedReviewID: nullable(reviewID), AcceptedAt: nullable(now), ID: plan.ID,
		}); err != nil {
			return err
		}
		if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		goal, err := f.q.GetAgentGoal(ctx, goalID)
		if err != nil {
			return err
		}
		return f.svc.appendGoalEvent(ctx, q, goalID, "goal_plan_approved", goal.Status, goal.Status, actor,
			map[string]any{"review_id": reviewID})
	})
}

// RejectGoalPlanReview closes a plan review as rejected and discards the pending
// edit. RequestChangesGoalPlanReview keeps the pending edit for re-submit. Both
// return the plan to draft and never touch content_json, so a rejected replan
// leaves a running goal's materialized work exactly as it was (BLOCKER 1).
func (f *ServiceFacade) RejectGoalPlanReview(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return f.decideGoalPlanReview(ctx, reviewID, ReviewRejected, summary, feedback, true, actor)
}

// RequestChangesGoalPlanReview returns the plan to draft keeping the pending edit.
func (f *ServiceFacade) RequestChangesGoalPlanReview(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return f.decideGoalPlanReview(ctx, reviewID, ReviewChangesRequested, summary, feedback, false, actor)
}

func (f *ServiceFacade) decideGoalPlanReview(ctx context.Context, reviewID, decision, summary, feedback string, discardPending bool, actor Actor) error {
	now := f.svc.now()
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		review, err := loadOpenPlanReview(ctx, q, reviewID)
		if err != nil {
			return err
		}
		goalID := review.GoalID.String
		plan, err := loadGoalPlan(ctx, q, goalID)
		if err != nil {
			return err
		}
		if _, err := q.SetAgentReviewDecision(ctx, sqlc.SetAgentReviewDecisionParams{
			Status: decision, Summary: summary, Feedback: feedback,
			ResolvedAt: nullable(now), UpdatedAt: now, ID: reviewID,
		}); err != nil {
			return err
		}
		if err := q.SetAgentGoalPlanStatus(ctx, sqlc.SetAgentGoalPlanStatusParams{
			Status: PlanStatusDraft, ID: plan.ID,
		}); err != nil {
			return err
		}
		if discardPending {
			if err := q.ClearAgentGoalPlanPending(ctx, plan.ID); err != nil {
				return err
			}
		}
		if err := q.SetAgentGoalActiveReview(ctx, sqlc.SetAgentGoalActiveReviewParams{
			ActiveReviewID: sql.NullString{}, UpdatedAt: now, ID: goalID,
		}); err != nil {
			return err
		}
		goal, err := f.q.GetAgentGoal(ctx, goalID)
		if err != nil {
			return err
		}
		return f.svc.appendGoalEvent(ctx, q, goalID, "goal_plan_review_"+decision, goal.Status, goal.Status, actor,
			map[string]any{"review_id": reviewID})
	})
}

// loadGoalPlan loads a goal's single plan row, mapping "no row" to the typed
// ErrGoalPlanNotFound.
func loadGoalPlan(ctx context.Context, q *sqlc.Queries, goalID string) (sqlc.AgentGoalPlan, error) {
	plan, err := q.GetAgentGoalPlanByGoal(ctx, goalID)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, ErrGoalPlanNotFound
	}
	return plan, err
}

// loadOpenPlanReview loads an open plan review by id, rejecting a missing,
// closed, or non-plan review with a typed error.
func loadOpenPlanReview(ctx context.Context, q *sqlc.Queries, reviewID string) (sqlc.AgentReview, error) {
	review, err := q.GetAgentReview(ctx, reviewID)
	if errors.Is(err, sql.ErrNoRows) {
		return review, ErrReviewNotFound
	}
	if err != nil {
		return review, err
	}
	if review.Status != ReviewRequested && review.Status != ReviewInProgress {
		return review, ErrReviewClosed
	}
	if review.Subject != ReviewSubjectPlan || !review.GoalID.Valid {
		return review, ErrPlanNotUnderReview
	}
	return review, nil
}

// pendingOrContent returns the pending edit when present, else the materialized
// content — the content under materialization.
func pendingOrContent(plan sqlc.AgentGoalPlan) string {
	if plan.PendingContentJson.Valid {
		return plan.PendingContentJson.String
	}
	return plan.ContentJson
}

// planningIfDraft maps a draft goal to planning (the status after a plan is
// staged) and leaves any other status as-is — for event audit accuracy.
func planningIfDraft(status string) string {
	if status == GoalStatusDraft {
		return GoalStatusPlanning
	}
	return status
}
