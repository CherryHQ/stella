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
