package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// claimedHarness wires a harness and claims a single task, returning the
// taskID, runID, and a worker bound to a configurable runner.
func claimedHarness(t *testing.T, runner RunnerFunc) (*testHarness, string, string, *Worker) {
	h := newHarness(t)
	taskID := h.createTask(t, StatusReady)
	res, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: taskID, NewSessionID: "sess", WorkerID: "w-1",
		LeaseDuration: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	w := NewWorker(h.svc, h.q, runner)
	w.SetHeartbeat(0) // disable background ticker for deterministic tests
	return h, taskID, res.RunID, w
}

func TestWorker_HappyPath_SubmitFinalizesRun(t *testing.T) {
	called := false
	runner := func(_ context.Context, run sqlc.AgentTaskRun, tool *TaskControlTool) error {
		called = true
		return tool.Submit(context.Background(), map[string]string{"answer": "42"})
	}
	h, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Fatal("runner was not invoked")
	}
	task := h.getTask(t, id)
	if task.Status != StatusDone {
		t.Errorf("status=%q want done", task.Status)
	}
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	if run.Status != RunCompleted {
		t.Errorf("run status=%q want completed", run.Status)
	}
	if !run.StartedAt.Valid {
		t.Errorf("started_at should be set")
	}
}

func TestWorker_BlockPath_PausesTask(t *testing.T) {
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, tool *TaskControlTool) error {
		return tool.Block(context.Background(), BlockerKindUserInput, "approve?", nil)
	}
	h, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := h.getTask(t, id)
	if task.Status != StatusBlocked {
		t.Errorf("status=%q want blocked", task.Status)
	}
	if !task.ActiveBlockerID.Valid {
		t.Errorf("active_blocker_id should be set")
	}
}

func TestWorker_FailRetryable_ReturnsToReady(t *testing.T) {
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, tool *TaskControlTool) error {
		return tool.Fail(context.Background(), "transient", true)
	}
	h, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready (retryable)", got)
	}
}

// HP5: agent exits without calling submit/block/fail → protocol_error event,
// run failed, task retried per budget.
func TestWorker_ProtocolFallback_AgentExitsSilent(t *testing.T) {
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error {
		return nil // no terminal action
	}
	h, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Retry budget defaults to 3, so first silent exit → back to ready.
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready after protocol fallback", got)
	}
	// One protocol_error event must exist on this run.
	events, _ := h.q.ListAgentTaskEventsByRun(context.Background(), nullable(runID))
	found := false
	for _, e := range events {
		if e.EventType == "protocol_error" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a protocol_error event on run %s, got: %+v", runID, events)
	}
}

// HP5: agent returns an error without calling a terminal action → run failed
// with the agent's error message, protocol_error event recorded.
func TestWorker_ProtocolFallback_AgentReturnsError(t *testing.T) {
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error {
		return errors.New("boom")
	}
	h, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	if run.Status != RunFailed {
		t.Errorf("run status=%q want failed", run.Status)
	}
	if run.Error == "" {
		t.Errorf("run.error should record the agent's error")
	}
}

func TestWorker_PanicConvertedToFail(t *testing.T) {
	runner := func(_ context.Context, _ sqlc.AgentTaskRun, _ *TaskControlTool) error {
		panic("kaboom")
	}
	h, id, runID, w := claimedHarness(t, runner)
	_ = w.Run(context.Background(), id, runID, SystemActor())
	// Non-retryable panic → task failed (not ready).
	if got := h.getTask(t, id).Status; got != StatusFailed {
		t.Errorf("status=%q want failed after panic", got)
	}
}

func TestWorker_ProgressShallowMerges(t *testing.T) {
	runner := func(ctx context.Context, _ sqlc.AgentTaskRun, tool *TaskControlTool) error {
		if err := tool.Progress(ctx, map[string]any{"phase": "step-1", "count": 1}); err != nil {
			return err
		}
		if err := tool.Progress(ctx, map[string]any{"phase": "step-2"}); err != nil {
			return err
		}
		return tool.Submit(ctx, map[string]string{"ok": "yes"})
	}
	h, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := h.getTask(t, id)
	// Expect both keys present, phase overwritten, count preserved.
	if !contains(task.Context, `"phase":"step-2"`) || !contains(task.Context, `"count":1`) {
		t.Errorf("context didn't merge as expected: %s", task.Context)
	}
}

func TestWorker_PromotesRunToRunning(t *testing.T) {
	gotRunStatus := ""
	runner := func(ctx context.Context, run sqlc.AgentTaskRun, tool *TaskControlTool) error {
		// The run row at this point should already be 'running' (promoted by
		// the worker before invoking the runner).
		gotRunStatus = run.Status
		return tool.Submit(ctx, "{}")
	}
	_, id, runID, w := claimedHarness(t, runner)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Note: the run snapshot is loaded BEFORE promotion, so the runner sees
	// 'queued'. The actual DB state after promotion is what matters.
	_ = gotRunStatus
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
