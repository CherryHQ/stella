package tasks

import (
	"context"
	"errors"
	"testing"
)

// Direct CreateGoal ("just do it") must land the goal in 'running' behind an
// accepted + materialized plan with exactly one ready child task — it
// auto-activates in the create tx, no separate activate step (#525 + direct
// auto-run).
func TestCreateGoal_Direct_MaterializesAndRunsOneChild(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()

	g, err := f.CreateGoal(ctx, CreateGoalInput{UserID: h.userID, AgentID: h.agentID, Title: "ship it"})
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if g.Status != GoalStatusRunning {
		t.Fatalf("goal status=%q want running", g.Status)
	}

	plan, err := h.q.GetAgentGoalPlanByGoal(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetAgentGoalPlanByGoal: %v", err)
	}
	if !plan.AcceptedAt.Valid {
		t.Errorf("plan accepted_at not set")
	}
	if !plan.MaterializedAt.Valid {
		t.Errorf("plan materialized_at not set")
	}
	if plan.PendingContentJson.Valid {
		t.Errorf("pending_content_json should be cleared after materialize, got %q", plan.PendingContentJson.String)
	}

	children, err := h.q.ListChildrenByGoal(ctx, nullable(g.ID))
	if err != nil {
		t.Fatalf("ListChildrenByGoal: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("children=%d want 1", len(children))
	}
	c := children[0]
	if c.Status != StatusReady {
		t.Errorf("child status=%q want ready (auto-activated)", c.Status)
	}
	if !c.SourcePlanID.Valid || c.SourcePlanID.String != plan.ID || c.PlanItemID != directPlanItemID {
		t.Errorf("child traceability = (%v,%q) want (%s,%s)", c.SourcePlanID, c.PlanItemID, plan.ID, directPlanItemID)
	}

	// Re-activating an already-running goal is a no-op error, not a double-run.
	if err := h.svc.ActivateGoal(ctx, g.ID, SystemActor()); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("ActivateGoal on running goal: got %v want ErrInvalidTransition", err)
	}
}

// A deferred goal stays in 'draft' with no plan row; activating it fails fast
// with ErrGoalPlanRequired — never a child-less running goal (opus S3).
func TestActivateGoal_Deferred_RequiresPlan(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	ctx := context.Background()

	g, err := f.CreateGoal(ctx, CreateGoalInput{UserID: h.userID, AgentID: h.agentID, Title: "deferred", PlanMode: PlanModeDeferred})
	if err != nil {
		t.Fatalf("CreateGoal(deferred): %v", err)
	}
	if g.Status != GoalStatusDraft {
		t.Fatalf("goal status=%q want draft (deferred)", g.Status)
	}
	if _, err := h.q.GetAgentGoalPlanByGoal(ctx, g.ID); err == nil {
		t.Fatalf("deferred goal should have no plan row")
	}
	if err := h.svc.ActivateGoal(ctx, g.ID, SystemActor()); !errors.Is(err, ErrGoalPlanRequired) {
		t.Fatalf("ActivateGoal(deferred): got %v want ErrGoalPlanRequired", err)
	}
	goal, _ := h.q.GetAgentGoal(ctx, g.ID)
	if goal.Status != GoalStatusDraft {
		t.Errorf("goal status=%q want draft (activation refused)", goal.Status)
	}
}

// CreateGoal rejects an unknown plan_mode at the boundary.
func TestCreateGoal_InvalidPlanMode_Rejected(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	_, err := f.CreateGoal(context.Background(), CreateGoalInput{
		UserID: h.userID, AgentID: h.agentID, Title: "g", PlanMode: "bogus",
	})
	if err == nil {
		t.Fatalf("want error for invalid plan_mode")
	}
}
