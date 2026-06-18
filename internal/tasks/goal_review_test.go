package tasks

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// insertGoalReview seeds an open agent_review row with goal_id set and points
// agent_goal.active_review_id at it. Returns the review id.
func (h *testHarness) insertGoalReview(t *testing.T, goalID, reviewerType string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().Format(time.RFC3339Nano)
	if _, err := h.q.CreateAgentReview(context.Background(), sqlc.CreateAgentReviewParams{
		ID:           id,
		GoalID:       sql.NullString{String: goalID, Valid: true},
		ReviewerType: reviewerType,
		Status:       ReviewRequested,
		Subject:      ReviewSubjectCompletion,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create goal review: %v", err)
	}
	if err := h.q.SetAgentGoalActiveReview(context.Background(), sqlc.SetAgentGoalActiveReviewParams{
		ActiveReviewID: sql.NullString{String: id, Valid: true},
		UpdatedAt:      now,
		ID:             goalID,
	}); err != nil {
		t.Fatalf("set goal active review: %v", err)
	}
	if _, err := h.q.TransitionAgentGoalStatus(context.Background(), sqlc.TransitionAgentGoalStatusParams{
		Status: GoalStatusReviewing, UpdatedAt: now, ID: goalID, Status_2: GoalStatusRunning,
	}); err != nil {
		t.Fatalf("transition goal to reviewing: %v", err)
	}
	return id
}

func TestApproveReview_GoalParent_GoalDone(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyHuman)
	rid := h.insertGoalReview(t, gid, ReviewerHuman)
	if err := h.svc.ApproveReview(context.Background(), rid, "lgtm", SystemActor()); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusDone {
		t.Errorf("goal status=%q want done", goal.Status)
	}
	if goal.ActiveReviewID.Valid {
		t.Errorf("active_review_id should be cleared")
	}
}

func TestRejectReview_GoalParent_GoalFailed(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyHuman)
	rid := h.insertGoalReview(t, gid, ReviewerHuman)
	if err := h.svc.RejectReview(context.Background(), rid, "no", "rationale", SystemActor()); err != nil {
		t.Fatalf("RejectReview: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusFailed {
		t.Errorf("goal status=%q want failed", goal.Status)
	}
}

func TestRequestChanges_GoalParent_TreatedAsFail(t *testing.T) {
	// Documented gap: no synthesizer retry budget yet, so request_changes on
	// a goal review collapses to fail. Test pins the behavior.
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyHuman)
	rid := h.insertGoalReview(t, gid, ReviewerHuman)
	if err := h.svc.RequestChanges(context.Background(), rid, "redo", "needs work", SystemActor()); err != nil {
		t.Fatalf("RequestChanges: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusFailed {
		t.Errorf("goal status=%q want failed (gap)", goal.Status)
	}
}

func TestEscalateReview_GoalParent_RepointsActiveReview(t *testing.T) {
	h := newHarness(t)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyAgent)
	rid := h.insertGoalReview(t, gid, ReviewerAgent)
	if err := h.svc.EscalateReview(context.Background(), rid, "out of scope", SystemActor()); err != nil {
		t.Fatalf("EscalateReview: %v", err)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if !goal.ActiveReviewID.Valid || goal.ActiveReviewID.String == rid {
		t.Errorf("goal active_review_id=%v want repointed to new human review", goal.ActiveReviewID)
	}
	if goal.Status != GoalStatusReviewing {
		t.Errorf("goal status=%q want still reviewing", goal.Status)
	}
}
