package tasks

import (
	"context"
	"testing"
	"time"
)

// Slice 5 (PR 7) acceptance tests. Soft dep semantics are implemented in
// readiness.go from Slice 1; these tests confirm the end-to-end behavior at
// the dispatcher level and document the contrast vs hard deps.

func forceTaskStatus(t *testing.T, h *testHarness, id, status string) {
	t.Helper()
	if _, err := h.db.Exec(`UPDATE agent_task SET status = $1 WHERE id = $2`, status, id); err != nil {
		t.Fatalf("force status: %v", err)
	}
}

func TestSoftDep_FailedUpstream_DownstreamDispatches(t *testing.T) {
	h, d := newDispatcherHarness(t, submitExec())
	upstream := h.createTask(t, StatusReady)
	downstream := h.createTask(t, StatusReady)
	if err := h.svc.AddDep(context.Background(), downstream, upstream, DepKindSoft, OnFailureBlock); err != nil {
		t.Fatalf("AddDep soft: %v", err)
	}
	forceTaskStatus(t, h, upstream, StatusFailed)

	d.Tick(context.Background())
	d.WaitIdle()

	// Soft dep on failed upstream → downstream still dispatchable.
	if got := h.getTask(t, downstream).Status; got != StatusDone {
		t.Errorf("downstream status=%q want done (soft dep ignores upstream failure)", got)
	}
}

func TestSoftDep_CancelledUpstream_DownstreamDispatches(t *testing.T) {
	h, d := newDispatcherHarness(t, submitExec())
	upstream := h.createTask(t, StatusReady)
	downstream := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), downstream, upstream, DepKindSoft, OnFailureBlock)
	forceTaskStatus(t, h, upstream, StatusCancelled)
	d.Tick(context.Background())
	d.WaitIdle()
	if got := h.getTask(t, downstream).Status; got != StatusDone {
		t.Errorf("downstream status=%q want done (soft dep on cancelled)", got)
	}
}

func TestSoftDep_PendingUpstream_DownstreamWaits(t *testing.T) {
	h, d := newDispatcherHarness(t, submitExec())
	upstream := h.createTask(t, StatusReady)
	downstream := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), downstream, upstream, DepKindSoft, OnFailureBlock)

	d.Tick(context.Background())
	d.WaitIdle()

	// Upstream completes on this tick; downstream waits one more tick because
	// its readiness was evaluated before upstream changed.
	if got := h.getTask(t, upstream).Status; got != StatusDone {
		t.Errorf("upstream status=%q want done", got)
	}
	if got := h.getTask(t, downstream).Status; got != StatusReady {
		t.Errorf("downstream status=%q want ready (still waiting on soft dep until upstream is terminal)", got)
	}
}

func TestSoftDep_OnFailureIgnored(t *testing.T) {
	// Per D11: on_failure is consulted only for hard deps. A soft dep with
	// on_failure='fail' must NOT propagate failure.
	h, d := newDispatcherHarness(t, submitExec())
	upstream := h.createTask(t, StatusReady)
	downstream := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), downstream, upstream, DepKindSoft, OnFailureFail)
	forceTaskStatus(t, h, upstream, StatusFailed)

	d.Tick(context.Background())
	d.WaitIdle()

	if got := h.getTask(t, downstream).Status; got != StatusDone {
		t.Errorf("downstream status=%q want done (on_failure ignored for soft)", got)
	}
}

func TestHardDep_OnFailureBlock_RequiresWaiverToProceed(t *testing.T) {
	// Tests the full waiver flow end-to-end via the dispatcher.
	h, d := newDispatcherHarness(t, submitExec())
	upstream := h.createTask(t, StatusReady)
	downstream := h.createTask(t, StatusReady)
	_ = h.svc.AddDep(context.Background(), downstream, upstream, DepKindHard, OnFailureBlock)
	forceTaskStatus(t, h, upstream, StatusFailed)

	d.Tick(context.Background())
	d.WaitIdle()
	// Without waiver: downstream blocked.
	if got := h.getTask(t, downstream).Status; got != StatusBlocked {
		t.Errorf("downstream status=%q want blocked", got)
	}

	// Waive → next tick dispatches.
	if err := h.svc.WaiveDep(context.Background(), downstream, upstream, h.userID, "force-through", SystemActor()); err != nil {
		t.Fatalf("WaiveDep: %v", err)
	}
	// Give the dispatcher a chance.
	d.Tick(context.Background())
	d.WaitIdle()
	if got := h.getTask(t, downstream).Status; got != StatusDone {
		t.Errorf("downstream status=%q want done after waiver", got)
	}
	_ = time.Second // silence unused-import if time becomes unused
}
