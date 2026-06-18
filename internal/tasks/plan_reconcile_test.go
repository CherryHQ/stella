package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Phase 3 (#525): the generic materializer is the single reconcile op driving
// both first-materialize and constrained replan (D7a). These tests exercise it
// directly — the public structured-plan write path lands in Phase 5 — by seeding
// a deferred goal and running materializeGoalPlanInTx through the same
// upsert-pending -> accept -> materialize tx the facade uses.

// materializeStructured upserts content as an accepted plan and runs the
// materializer in one tx, mirroring createAndAcceptDirectPlanInTx for tests.
// It reloads the goal so a replan on a running goal sees the live status.
func (h *testHarness) materializeStructured(f *ServiceFacade, goalID string, content PlanContent) error {
	ctx := context.Background()
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	goal, err := h.q.GetAgentGoal(ctx, goalID)
	if err != nil {
		return err
	}
	// Sessions are pre-minted outside the tx (SQLite single-writer), keyed by
	// item id. Re-minting on a replan is harmless: only new items consume one.
	sessions := make(map[string]string, len(content.Items))
	for _, it := range content.Items {
		sid, err := f.newSession(ctx, goal.UserID, goal.AgentID, goal.ProjectID.String)
		if err != nil {
			return err
		}
		sessions[it.ID] = sid
	}
	now := f.svc.now()
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		if err := q.UpsertAgentGoalPlanPending(ctx, sqlc.UpsertAgentGoalPlanPendingParams{
			ID:                 uuid.NewString(),
			GoalID:             goal.ID,
			Status:             PlanStatusAccepted,
			ReviewPolicy:       ReviewPolicyNone,
			PendingContentJson: nullable(string(raw)),
		}); err != nil {
			return err
		}
		plan, err := q.GetAgentGoalPlanByGoal(ctx, goal.ID)
		if err != nil {
			return err
		}
		if err := q.SetAgentGoalPlanAccepted(ctx, sqlc.SetAgentGoalPlanAcceptedParams{
			Status:     PlanStatusAccepted,
			AcceptedAt: nullable(now),
			ID:         plan.ID,
		}); err != nil {
			return err
		}
		plan, err = q.GetAgentGoalPlanByGoal(ctx, goal.ID)
		if err != nil {
			return err
		}
		return f.materializeGoalPlanInTx(ctx, q, goal, plan, sessions, now)
	})
}

// deferredGoal creates a draft goal with no plan row to drive structured replans.
func (h *testHarness) deferredGoal(t *testing.T, f *ServiceFacade) sqlc.AgentGoal {
	t.Helper()
	g, err := f.CreateGoal(context.Background(), CreateGoalInput{
		UserID: h.userID, AgentID: h.agentID, Title: "structured", PlanMode: PlanModeDeferred,
	})
	if err != nil {
		t.Fatalf("CreateGoal(deferred): %v", err)
	}
	return g
}

// planTasks returns the goal's plan-backed tasks keyed by plan_item_id.
func (h *testHarness) planTasks(t *testing.T, goalID string) map[string]sqlc.AgentTask {
	t.Helper()
	rows, err := h.q.ListChildrenByGoal(context.Background(), nullable(goalID))
	if err != nil {
		t.Fatalf("ListChildrenByGoal: %v", err)
	}
	out := make(map[string]sqlc.AgentTask, len(rows))
	for _, r := range rows {
		if r.PlanItemID != "" {
			out[r.PlanItemID] = r
		}
	}
	return out
}

func (h *testHarness) depItemIDs(t *testing.T, taskID string, byTask map[string]string) []string {
	t.Helper()
	deps, err := h.q.ListAgentTaskDeps(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListAgentTaskDeps: %v", err)
	}
	var ids []string
	for _, d := range deps {
		ids = append(ids, byTask[d.DepTaskID])
	}
	sort.Strings(ids)
	return ids
}

func (h *testHarness) forceStatus(t *testing.T, taskID, status string) {
	t.Helper()
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE agent_task SET status = ? WHERE id = ?`, status, taskID); err != nil {
		t.Fatalf("force status %s: %v", status, err)
	}
}

// structured returns a design->impl->verify plan whose deps form impl<-design,
// verify<-impl (a valid structured plan).
func structuredPlan() PlanContent {
	return PlanContent{Items: []PlanItem{
		{ID: "d", Title: "design", Role: PlanRoleDesign},
		{ID: "i", Title: "impl", Role: PlanRoleImpl, Deps: []string{"d"}, Criteria: []string{"compiles"}},
		{ID: "v", Title: "verify", Role: PlanRoleVerify, Deps: []string{"i"}},
	}}
}

// A first materialize of a structured plan creates one task per item, wires deps
// from item.Deps, writes criteria rows, and promotes the goal to planned.
func TestMaterialize_Structured_BuildsTaskGraph(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)

	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	tasks := h.planTasks(t, g.ID)
	if len(tasks) != 3 {
		t.Fatalf("tasks=%d want 3", len(tasks))
	}
	byTask := map[string]string{}
	for item, tk := range tasks {
		byTask[tk.ID] = item
		if tk.Status != StatusDraft {
			t.Errorf("item %q status=%q want draft", item, tk.Status)
		}
	}
	if got := h.depItemIDs(t, tasks["i"].ID, byTask); len(got) != 1 || got[0] != "d" {
		t.Errorf("impl deps=%v want [d]", got)
	}
	if got := h.depItemIDs(t, tasks["v"].ID, byTask); len(got) != 1 || got[0] != "i" {
		t.Errorf("verify deps=%v want [i]", got)
	}
	crit, err := h.q.ListAgentTaskCriteria(context.Background(), tasks["i"].ID)
	if err != nil {
		t.Fatalf("ListAgentTaskCriteria: %v", err)
	}
	if len(crit) != 1 || crit[0].Description != "compiles" {
		t.Errorf("impl criteria=%+v want [compiles]", crit)
	}
	goal, _ := h.q.GetAgentGoal(context.Background(), g.ID)
	if goal.Status != GoalStatusPlanned {
		t.Errorf("goal status=%q want planned", goal.Status)
	}
}

// Re-materializing the identical plan is idempotent: same task ids, no
// duplicates, deps unchanged (D7).
func TestMaterialize_Idempotent(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)

	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize#1: %v", err)
	}
	first := h.planTasks(t, g.ID)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize#2: %v", err)
	}
	second := h.planTasks(t, g.ID)

	if len(second) != 3 {
		t.Fatalf("tasks after re-materialize=%d want 3", len(second))
	}
	ctx := context.Background()
	for item, tk := range first {
		if second[item].ID != tk.ID {
			t.Errorf("item %q task id changed %s -> %s", item, tk.ID, second[item].ID)
		}
		if second[item].UpdatedAt != tk.UpdatedAt {
			t.Errorf("item %q updated_at churned on idempotent re-materialize", item)
		}
	}
	// Deps and criteria must be stable, not deleted-and-recreated.
	deps, _ := h.q.ListAgentTaskDeps(ctx, second["i"].ID)
	if len(deps) != 1 {
		t.Errorf("impl deps=%d want 1 after re-materialize", len(deps))
	}
	crit, _ := h.q.ListAgentTaskCriteria(ctx, second["i"].ID)
	if len(crit) != 1 {
		t.Errorf("impl criteria=%d want 1 after re-materialize", len(crit))
	}
}

// A replan that adds a verify item creates only the new task and wires its dep,
// leaving existing tasks intact.
func TestReplan_AddItem(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	before := h.planTasks(t, g.ID)

	plan := structuredPlan()
	plan.Items = append(plan.Items, PlanItem{ID: "v2", Title: "verify2", Role: PlanRoleVerify, Deps: []string{"i"}})
	if err := h.materializeStructured(f, g.ID, plan); err != nil {
		t.Fatalf("replan(add): %v", err)
	}

	after := h.planTasks(t, g.ID)
	if len(after) != 4 {
		t.Fatalf("tasks=%d want 4", len(after))
	}
	if after["i"].ID != before["i"].ID {
		t.Errorf("impl task id changed on add")
	}
	byTask := map[string]string{}
	for item, tk := range after {
		byTask[tk.ID] = item
	}
	if got := h.depItemIDs(t, after["v2"].ID, byTask); len(got) != 1 || got[0] != "i" {
		t.Errorf("v2 deps=%v want [i]", got)
	}
}

// A replan that edits a not-started item's title/criteria updates the task in
// place and replaces its criteria rows.
func TestReplan_ModifyNotStarted(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	id := h.planTasks(t, g.ID)["i"].ID

	plan := structuredPlan()
	plan.Items[1].Title = "impl v2"
	plan.Items[1].Criteria = []string{"compiles", "tests pass"}
	if err := h.materializeStructured(f, g.ID, plan); err != nil {
		t.Fatalf("replan(modify): %v", err)
	}

	tk := h.getTask(t, id)
	if tk.Title != "impl v2" {
		t.Errorf("title=%q want %q", tk.Title, "impl v2")
	}
	crit, _ := h.q.ListAgentTaskCriteria(context.Background(), id)
	if len(crit) != 2 || crit[0].Description != "compiles" || crit[1].Description != "tests pass" {
		t.Errorf("criteria=%+v want [compiles, tests pass]", crit)
	}
}

// A replan that drops a not-started item cancels its task and cleans up the
// dangling dep edges that pointed at it.
func TestReplan_RemoveNotStarted_Cancels(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// Drop the design item (a not-started leaf upstream of impl). impl keeps its
	// downstream verify, so the reduced plan stays structurally valid.
	plan := PlanContent{Items: []PlanItem{
		{ID: "i", Title: "impl", Role: PlanRoleImpl, Criteria: []string{"compiles"}},
		{ID: "v", Title: "verify", Role: PlanRoleVerify, Deps: []string{"i"}},
	}}
	if err := h.materializeStructured(f, g.ID, plan); err != nil {
		t.Fatalf("replan(remove): %v", err)
	}

	tasks := h.planTasks(t, g.ID)
	if _, ok := tasks["d"]; ok && tasks["d"].Status != StatusCancelled {
		t.Errorf("design task status=%q want cancelled", tasks["d"].Status)
	}
	// impl no longer depends on the removed design item.
	byTask := map[string]string{}
	for item, tk := range tasks {
		byTask[tk.ID] = item
	}
	if got := h.depItemIDs(t, tasks["i"].ID, byTask); len(got) != 0 {
		t.Errorf("impl deps=%v want [] after design removed", got)
	}
}

// A replan that drops an item whose task already produced output detaches it
// (required=0, detached_at set) instead of cancelling — its output survives.
func TestReplan_RemoveWithOutput_Detaches(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	dID := h.planTasks(t, g.ID)["d"].ID
	h.forceStatus(t, dID, StatusDone)

	plan := PlanContent{Items: []PlanItem{
		{ID: "i", Title: "impl", Role: PlanRoleImpl, Criteria: []string{"compiles"}},
		{ID: "v", Title: "verify", Role: PlanRoleVerify, Deps: []string{"i"}},
	}}
	if err := h.materializeStructured(f, g.ID, plan); err != nil {
		t.Fatalf("replan(remove-with-output): %v", err)
	}

	d := h.getTask(t, dID)
	if d.Status != StatusDone {
		t.Errorf("detached task status=%q want done (preserved)", d.Status)
	}
	if !d.DetachedAt.Valid {
		t.Errorf("detached_at not set")
	}
	if d.Required != 0 {
		t.Errorf("required=%d want 0", d.Required)
	}
	// Plan link is kept so the task still routes through handoff/context
	// enforcement and traces back to its origin item (codex BLOCKER 2).
	if !d.SourcePlanID.Valid || d.PlanItemID != "d" {
		t.Errorf("traceability lost: source_plan=%v item=%q", d.SourcePlanID, d.PlanItemID)
	}
}

// Editing the definition of an item whose task is already in flight aborts the
// whole replan with ErrPlanItemInFlight and persists nothing (D7a).
func TestReplan_ModifyInFlight_Rejected(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	id := h.planTasks(t, g.ID)["i"].ID
	h.forceStatus(t, id, StatusRunning)

	plan := structuredPlan()
	plan.Items[1].Title = "impl edited"
	err := h.materializeStructured(f, g.ID, plan)
	if !errors.Is(err, ErrPlanItemInFlight) {
		t.Fatalf("replan(modify in-flight): got %v want ErrPlanItemInFlight", err)
	}
	if got := h.getTask(t, id).Title; got != "impl" {
		t.Errorf("title=%q want unchanged %q (tx rolled back)", got, "impl")
	}
}

// An in-flight item left untouched does not block a replan that only adds work;
// the running task is preserved and the new item is wired to it.
func TestReplan_InFlightUnchanged_AllowsAdd(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	iID := h.planTasks(t, g.ID)["i"].ID
	h.forceStatus(t, iID, StatusRunning)

	plan := structuredPlan()
	plan.Items = append(plan.Items, PlanItem{ID: "v2", Title: "verify2", Role: PlanRoleVerify, Deps: []string{"i"}})
	if err := h.materializeStructured(f, g.ID, plan); err != nil {
		t.Fatalf("replan(add beside in-flight): %v", err)
	}

	tasks := h.planTasks(t, g.ID)
	if tasks["i"].Status != StatusRunning {
		t.Errorf("impl status=%q want running (untouched)", tasks["i"].Status)
	}
	if _, ok := tasks["v2"]; !ok {
		t.Errorf("v2 task not created")
	}
}

// A replan that drops one of an item's deps while keeping the upstream item as
// its own task deletes only the stale edge (2nd-pass SF1).
func TestReplan_StaleDep_Deleted(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)

	// verify depends on BOTH design and impl initially.
	fanIn := PlanContent{Items: []PlanItem{
		{ID: "d", Title: "design", Role: PlanRoleDesign},
		{ID: "i", Title: "impl", Role: PlanRoleImpl, Deps: []string{"d"}},
		{ID: "v", Title: "verify", Role: PlanRoleVerify, Deps: []string{"d", "i"}},
	}}
	if err := h.materializeStructured(f, g.ID, fanIn); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got := len(mustDeps(t, h, h.planTasks(t, g.ID)["v"].ID)); got != 2 {
		t.Fatalf("verify deps=%d want 2 initially", got)
	}

	// Replan: verify now depends on impl only; design stays a task (impl needs it).
	fanIn.Items[2].Deps = []string{"i"}
	if err := h.materializeStructured(f, g.ID, fanIn); err != nil {
		t.Fatalf("replan(drop dep): %v", err)
	}

	tasks := h.planTasks(t, g.ID)
	byTask := map[string]string{}
	for item, tk := range tasks {
		byTask[tk.ID] = item
	}
	if _, ok := tasks["d"]; !ok {
		t.Fatalf("design task removed but should survive (impl depends on it)")
	}
	if got := h.depItemIDs(t, tasks["v"].ID, byTask); len(got) != 1 || got[0] != "i" {
		t.Errorf("verify deps=%v want [i] after dropping design edge", got)
	}
}

func mustDeps(t *testing.T, h *testHarness, taskID string) []sqlc.AgentTaskDep {
	t.Helper()
	deps, err := h.q.ListAgentTaskDeps(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListAgentTaskDeps: %v", err)
	}
	return deps
}

// A replan on a running goal does not demote it back to planned (opus B2).
func TestReplan_RunningGoal_StaysRunning(t *testing.T) {
	h := newHarness(t)
	f := NewServiceFacade(h.db, h.q, h.svc, h.sessionMinter())
	g := h.deferredGoal(t, f)
	if err := h.materializeStructured(f, g.ID, structuredPlan()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := h.svc.ActivateGoal(context.Background(), g.ID, SystemActor()); err != nil {
		t.Fatalf("ActivateGoal: %v", err)
	}

	plan := structuredPlan()
	plan.Items = append(plan.Items, PlanItem{ID: "v2", Title: "verify2", Role: PlanRoleVerify, Deps: []string{"i"}})
	if err := h.materializeStructured(f, g.ID, plan); err != nil {
		t.Fatalf("replan(running goal): %v", err)
	}

	goal, _ := h.q.GetAgentGoal(context.Background(), g.ID)
	if goal.Status != GoalStatusRunning {
		t.Errorf("goal status=%q want running (not demoted)", goal.Status)
	}
}
