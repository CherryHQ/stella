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

func TestDispatcher_RollupGoals_FailedGoalRecoversWhenChildCompletes(t *testing.T) {
	h, d := newDispatcherHarness(t, nil)
	gid := h.createGoal(t, GoalStatusFailed, ReviewPolicyNone)
	child := h.createChildTask(t, gid, StatusFailed)

	goalStatus := func() string {
		g, _ := h.q.GetAgentGoal(context.Background(), gid)
		return g.Status
	}

	// A still-failed required child keeps the goal failed.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusFailed {
		t.Fatalf("with failed child: goal=%q want failed", got)
	}

	// Operational failure was reconciled elsewhere: the child is now complete.
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE agent_task SET status = 'done' WHERE id = $1`, child); err != nil {
		t.Fatalf("complete child: %v", err)
	}

	// The next rollup should recover the parent all the way to done instead of
	// leaving it permanently stuck in failed.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusDone {
		t.Fatalf("after child completion: goal=%q want done", got)
	}
}

// The realistic #425 recovery path: a failed required child is reopened (back
// to ready/pending), not completed in place. Rollup must take the failed goal
// through running (UnblockGoal) before it can complete on a later tick.
func TestDispatcher_RollupGoals_FailedGoalRecoversToRunningWhenChildReopens(t *testing.T) {
	h, d := newDispatcherHarness(t, submitExec())
	gid := h.createGoal(t, GoalStatusFailed, ReviewPolicyNone)
	child := h.createChildTask(t, gid, StatusFailed)

	goalStatus := func() string {
		g, _ := h.q.GetAgentGoal(context.Background(), gid)
		return g.Status
	}

	// Tick 1: a still-failed required child keeps the goal failed.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusFailed {
		t.Fatalf("with failed child: goal=%q want failed", got)
	}

	// The failed child is reopened and returns to ready (pending), not done.
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE agent_task SET status = 'ready' WHERE id = $1`, child); err != nil {
		t.Fatalf("reopen child: %v", err)
	}

	// Tick 2: rollup runs before dispatch, sees a pending required child, and
	// recovers the goal to running via UnblockGoal (failed -> running). The
	// child is then dispatched and submits.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusRunning {
		t.Fatalf("after reopen: goal=%q want running", got)
	}

	// Tick 3: the recovered child is done -> goal completes.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := goalStatus(); got != GoalStatusDone {
		t.Fatalf("after completion tick: goal=%q want done", got)
	}
}

// A cancelled required child can never be reopened, so a goal whose remaining
// required children are {done, cancelled} must fail rather than silently
// reporting success.
func TestDispatcher_RollupGoals_CancelledRequiredChildFailsGoal(t *testing.T) {
	h, d := newDispatcherHarness(t, nil)
	gid := h.createGoal(t, GoalStatusRunning, ReviewPolicyNone)
	h.createChildTask(t, gid, StatusDone)
	h.createChildTask(t, gid, StatusCancelled)

	d.Tick(context.Background())
	d.WaitIdle()
	goal, _ := h.q.GetAgentGoal(context.Background(), gid)
	if goal.Status != GoalStatusFailed {
		t.Fatalf("goal with a cancelled required child: status=%q want failed", goal.Status)
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
		`UPDATE agent_task SET status = 'ready' WHERE id = $1`, child); err != nil {
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
