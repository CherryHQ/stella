package tasks

import (
	"context"
	"testing"
)

func TestDispatcher_RollupGoals_CompletesWhenAllChildrenDoneNoPolicy(t *testing.T) {
	h, d := newDispatcherHarness(t, nil)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	h.createChildTask(t, gid, StatusDone)
	d.Tick(context.Background())
	d.WaitIdle()
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusDone {
		t.Errorf("goal status=%q want done", goal.Status)
	}
}

// A goal blocked by a required child recovers once that child's blocker is
// resolved (child returns to ready). Rollup unblocks the goal on the next
// tick and the goal completes after the child finishes.
func TestDispatcher_RollupGoals_BlockedGoalRecoversWhenChildUnblocks(t *testing.T) {
	h, d := newDispatcherHarness(t, submitExec())
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	child := h.createChildTask(t, gid, StatusBlocked)

	goalStatus := func() string {
		g, _ := h.q.GetAgentGoal(context.Background(), gid)
		return g.Status
	}

	// Tick 1: a required child is blocked -> goal blocks.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusBlocked {
		t.Fatalf("after block tick: goal=%q want blocked", got)
	}

	// Child blocker resolved: the child task returns to ready.
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE agent_task SET status = 'ready' WHERE id = ?`, child); err != nil {
		t.Fatalf("unblock child: %v", err)
	}

	// Tick 2: rollup runs before dispatch, sees no blocked child with work
	// pending -> goal recovers to running; the child is then dispatched.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusRunning {
		t.Fatalf("after recovery tick: goal=%q want running", got)
	}

	// Tick 3: child has submitted (done) -> goal completes.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusDone {
		t.Errorf("after completion tick: goal=%q want done", got)
	}
}
