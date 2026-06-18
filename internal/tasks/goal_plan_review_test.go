package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Phase 5 (#525): the structured-plan lifecycle and the dedicated plan-review
// path. The throughline of every test is that a plan review accepts a *plan* and
// never marks a *goal* done, and that promotion happens only at materialize — so
// approve/reject never mutate content_json or the live task graph.

// planContentWith returns structuredPlan() plus the extra items, for replans.
func planContentWith(extra ...PlanItem) PlanContent {
	c := structuredPlan()
	c.Items = append(c.Items, extra...)
	return c
}

// AcceptGoalPlan (review_policy none) accepts the pending edit without promoting:
// the plan goes accepted, the goal stays planning, and only the subsequent
// MaterializeGoalPlan builds the task graph and promotes the goal to planned.
func TestAcceptGoalPlan_NoReview_ThenMaterialize(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g := h.deferredGoal(t, f)

	if err := f.CreateGoalPlan(ctx, g.ID, structuredPlan(), ReviewPolicyNone, SystemActor()); err != nil {
		t.Fatalf("CreateGoalPlan: %v", err)
	}
	if got := h.goalStatus(t, g.ID); got != GoalStatusPlanning {
		t.Errorf("goal status after create=%q want planning", got)
	}
	if err := f.AcceptGoalPlan(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("AcceptGoalPlan: %v", err)
	}
	plan := h.plan(t, g.ID)
	if plan.Status != PlanStatusAccepted {
		t.Errorf("plan status=%q want accepted", plan.Status)
	}
	if plan.MaterializedAt.Valid {
		t.Errorf("plan materialized before MaterializeGoalPlan")
	}
	if got := h.goalStatus(t, g.ID); got != GoalStatusPlanning {
		t.Errorf("goal status after accept=%q want planning (not yet planned)", got)
	}

	if err := f.MaterializeGoalPlan(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("MaterializeGoalPlan: %v", err)
	}
	if len(h.planTasks(t, g.ID)) != 3 {
		t.Errorf("plan tasks=%d want 3", len(h.planTasks(t, g.ID)))
	}
	if got := h.goalStatus(t, g.ID); got != GoalStatusPlanned {
		t.Errorf("goal status after materialize=%q want planned", got)
	}
}

// The human-review path: submit opens a subject='plan' review and parks the plan
// in_review; approve stamps the plan approved + accepted_at + approved_review_id,
// clears the goal's active review, and — critically — does NOT mark the goal done
// or promote it. Materialize after approval promotes to planned.
func TestSubmitApprovePlanReview_Lifecycle(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g := h.deferredGoal(t, f)

	if err := f.CreateGoalPlan(ctx, g.ID, structuredPlan(), ReviewPolicyHuman, SystemActor()); err != nil {
		t.Fatalf("CreateGoalPlan: %v", err)
	}
	reviewID, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor())
	if err != nil {
		t.Fatalf("SubmitGoalPlanForReview: %v", err)
	}
	if h.plan(t, g.ID).Status != PlanStatusInReview {
		t.Errorf("plan status=%q want in_review", h.plan(t, g.ID).Status)
	}
	rev, _ := h.q.GetAgentReview(ctx, reviewID)
	if rev.Subject != ReviewSubjectPlan {
		t.Errorf("review subject=%q want plan", rev.Subject)
	}
	goal, _ := h.q.GetAgentGoal(ctx, g.ID)
	if !goal.ActiveReviewID.Valid || goal.ActiveReviewID.String != reviewID {
		t.Errorf("goal active_review_id=%v want %s", goal.ActiveReviewID, reviewID)
	}
	if goal.Status != GoalStatusPlanning {
		t.Errorf("goal status=%q want planning", goal.Status)
	}

	if err := f.ApproveGoalPlanReview(ctx, reviewID, "lgtm", SystemActor()); err != nil {
		t.Fatalf("ApproveGoalPlanReview: %v", err)
	}
	plan := h.plan(t, g.ID)
	if plan.Status != PlanStatusApproved {
		t.Errorf("plan status=%q want approved", plan.Status)
	}
	if !plan.ApprovedReviewID.Valid || plan.ApprovedReviewID.String != reviewID {
		t.Errorf("approved_review_id=%v want %s", plan.ApprovedReviewID, reviewID)
	}
	if !plan.AcceptedAt.Valid {
		t.Errorf("accepted_at not stamped on approve")
	}
	goal, _ = h.q.GetAgentGoal(ctx, g.ID)
	if goal.Status != GoalStatusPlanning {
		t.Errorf("goal status=%q want planning (approve must not promote/finish)", goal.Status)
	}
	if goal.ActiveReviewID.Valid {
		t.Errorf("active_review_id not cleared after approve")
	}

	if err := f.MaterializeGoalPlan(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("MaterializeGoalPlan: %v", err)
	}
	if got := h.goalStatus(t, g.ID); got != GoalStatusPlanned {
		t.Errorf("goal status after materialize=%q want planned", got)
	}
}

// Regression guard for the ApproveReview footgun: the generic completion-review
// API must refuse a subject='plan' review with the typed redirect error and leave
// the goal status untouched, so a plan review can never mark a goal done.
func TestGenericApproveReview_OnPlanReview_Rejected(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g := h.deferredGoal(t, f)
	if err := f.CreateGoalPlan(ctx, g.ID, structuredPlan(), ReviewPolicyHuman, SystemActor()); err != nil {
		t.Fatalf("CreateGoalPlan: %v", err)
	}
	reviewID, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor())
	if err != nil {
		t.Fatalf("SubmitGoalPlanForReview: %v", err)
	}

	// Every generic completion-review verb must refuse the plan review identically
	// and leave the goal + plan untouched (the guard returns before any mutation).
	generic := map[string]func() error{
		"ApproveReview":  func() error { return f.ApproveReview(ctx, reviewID, "x", SystemActor()) },
		"RejectReview":   func() error { return f.RejectReview(ctx, reviewID, "x", "y", SystemActor()) },
		"RequestChanges": func() error { return f.RequestChanges(ctx, reviewID, "x", "y", SystemActor()) },
	}
	for name, call := range generic {
		if err := call(); !errors.Is(err, ErrPlanReviewWrongPath) {
			t.Errorf("%s on plan review: got %v want ErrPlanReviewWrongPath", name, err)
		}
		if got := h.goalStatus(t, g.ID); got != GoalStatusPlanning {
			t.Errorf("%s: goal status=%q want planning unchanged", name, got)
		}
		if h.plan(t, g.ID).Status != PlanStatusInReview {
			t.Errorf("%s: plan status changed by refused generic decision", name)
		}
	}
}

// Only one open plan review per goal: a second submit is refused, and a
// CreateGoalPlan upsert against an in_review plan is refused too (no clobber).
func TestPlanReview_SecondOpen_And_ClobberRefused(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g := h.deferredGoal(t, f)
	if err := f.CreateGoalPlan(ctx, g.ID, structuredPlan(), ReviewPolicyHuman, SystemActor()); err != nil {
		t.Fatalf("CreateGoalPlan: %v", err)
	}
	if _, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("SubmitGoalPlanForReview: %v", err)
	}

	if _, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor()); !errors.Is(err, ErrPlanReviewExists) {
		t.Errorf("second submit: got %v want ErrPlanReviewExists", err)
	}
	if err := f.CreateGoalPlan(ctx, g.ID, structuredPlan(), ReviewPolicyHuman, SystemActor()); !errors.Is(err, ErrPlanReviewExists) {
		t.Errorf("create against in_review: got %v want ErrPlanReviewExists", err)
	}
}

// changes_requested returns the plan to draft while keeping the pending edit, so
// the same edit can be re-submitted for review.
func TestRequestChanges_BackToDraft_Resubmit(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g := h.deferredGoal(t, f)
	if err := f.CreateGoalPlan(ctx, g.ID, structuredPlan(), ReviewPolicyHuman, SystemActor()); err != nil {
		t.Fatalf("CreateGoalPlan: %v", err)
	}
	reviewID, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor())
	if err != nil {
		t.Fatalf("SubmitGoalPlanForReview: %v", err)
	}

	if err := f.RequestChangesGoalPlanReview(ctx, reviewID, "tighten scope", "see notes", SystemActor()); err != nil {
		t.Fatalf("RequestChangesGoalPlanReview: %v", err)
	}
	plan := h.plan(t, g.ID)
	if plan.Status != PlanStatusDraft {
		t.Errorf("plan status=%q want draft", plan.Status)
	}
	if !plan.PendingContentJson.Valid {
		t.Errorf("pending edit discarded on changes-requested (should be kept)")
	}
	goal, _ := h.q.GetAgentGoal(ctx, g.ID)
	if goal.ActiveReviewID.Valid {
		t.Errorf("active_review_id not cleared after changes-requested")
	}

	// The kept edit re-submits cleanly into a fresh review.
	if _, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("re-submit after changes-requested: %v", err)
	}
}

// Replan-under-review on a running goal (BLOCKER 1): the goal keeps running on its
// materialized content while the edit is reviewed, and rejecting the replan leaves
// content_json and the live task graph exactly as they were — no rejected-edit leak.
func TestReplanUnderReview_RunningGoal_Reject_LeavesUntouched(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := h.svc.ActivateGoal(ctx, g.ID, SystemActor()); err != nil {
		t.Fatalf("ActivateGoal: %v", err)
	}
	contentBefore := h.plan(t, g.ID).ContentJson
	tasksBefore := len(h.planTasks(t, g.ID))

	// Stage a replan that would add a task, route it through human review.
	if err := f.CreateGoalPlan(ctx, g.ID,
		planContentWith(PlanItem{ID: "v2", Title: "verify2", Role: PlanRoleVerify, Deps: []string{"i"}}),
		ReviewPolicyHuman, SystemActor()); err != nil {
		t.Fatalf("CreateGoalPlan(replan): %v", err)
	}
	reviewID, err := f.SubmitGoalPlanForReview(ctx, g.ID, SystemActor())
	if err != nil {
		t.Fatalf("SubmitGoalPlanForReview(replan): %v", err)
	}
	if got := h.goalStatus(t, g.ID); got != GoalStatusRunning {
		t.Errorf("goal status under replan review=%q want running", got)
	}
	if got := len(h.planTasks(t, g.ID)); got != tasksBefore {
		t.Errorf("task graph changed under review: %d want %d", got, tasksBefore)
	}

	if err := f.RejectGoalPlanReview(ctx, reviewID, "no", "", SystemActor()); err != nil {
		t.Fatalf("RejectGoalPlanReview: %v", err)
	}
	plan := h.plan(t, g.ID)
	if plan.Status != PlanStatusDraft {
		t.Errorf("plan status=%q want draft after reject", plan.Status)
	}
	if plan.PendingContentJson.Valid {
		t.Errorf("rejected pending edit not discarded")
	}
	if plan.ContentJson != contentBefore {
		t.Errorf("content_json mutated by rejected replan")
	}
	if got := len(h.planTasks(t, g.ID)); got != tasksBefore {
		t.Errorf("task graph changed by rejected replan: %d want %d", got, tasksBefore)
	}
	if got := h.goalStatus(t, g.ID); got != GoalStatusRunning {
		t.Errorf("goal status=%q want running after reject", got)
	}
}

// goalStatus and plan are small accessors for Phase 5 assertions.
func (h *testHarness) goalStatus(t *testing.T, goalID string) string {
	t.Helper()
	g, err := h.q.GetAgentGoal(context.Background(), goalID)
	if err != nil {
		t.Fatalf("GetAgentGoal: %v", err)
	}
	return g.Status
}

func (h *testHarness) plan(t *testing.T, goalID string) sqlc.AgentGoalPlan {
	t.Helper()
	p, err := h.q.GetAgentGoalPlanByGoal(context.Background(), goalID)
	if err != nil {
		t.Fatalf("GetAgentGoalPlanByGoal: %v", err)
	}
	return p
}
