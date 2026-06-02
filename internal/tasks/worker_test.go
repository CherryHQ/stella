package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

// claimedHarness wires a harness and claims a single task, returning the
// taskID, runID, and a worker bound to a configurable executor.
func claimedHarness(t *testing.T, exec Executor) (*testHarness, string, string, *Worker) {
	h := newHarness(t)
	taskID := h.createTask(t, StatusReady)
	res, err := h.svc.Claim(context.Background(), ClaimParams{
		TaskID: taskID, NewSessionID: "sess", WorkerID: "w-1",
		LeaseDuration: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	w := NewWorker(h.svc, h.q, exec)
	w.SetHeartbeat(0) // disable background ticker for deterministic tests
	return h, taskID, res.RunID, w
}

func TestWorker_HappyPath_SubmitFinalizesRun(t *testing.T) {
	called := false
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		called = true
		return Result{Action: TerminalSubmit, Output: map[string]string{"answer": "42"}}, nil
	})
	h, id, runID, w := claimedHarness(t, exec)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Fatal("executor was not invoked")
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
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		return Result{Action: TerminalBlock, Blocker: &BlockerResult{
			Kind: BlockerKindUserInput, Question: "approve?",
		}}, nil
	})
	h, id, runID, w := claimedHarness(t, exec)
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
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		return Result{Action: TerminalFail, Failure: &FailureResult{Reason: "transient", Retryable: true}}, nil
	})
	h, id, runID, w := claimedHarness(t, exec)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.getTask(t, id).Status; got != StatusReady {
		t.Errorf("status=%q want ready (retryable)", got)
	}
}

// HP5: executor reports no terminal action → protocol_error event, run failed,
// task retried per budget.
func TestWorker_ProtocolFallback_NoTerminal(t *testing.T) {
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		return Result{Action: TerminalNone}, nil
	})
	h, id, runID, w := claimedHarness(t, exec)
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

// An executor that returns an error is unexpected; the worker converts it to a
// retryable failure and records the error on the run.
func TestWorker_ExecutorError_FailsRetryable(t *testing.T) {
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, errors.New("boom")
	})
	h, id, runID, w := claimedHarness(t, exec)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	if run.Status != RunFailed {
		t.Errorf("run status=%q want failed", run.Status)
	}
	if run.Error == "" {
		t.Errorf("run.error should record the executor error")
	}
}

func TestWorker_PanicConvertedToFail(t *testing.T) {
	exec := executorFunc(func(_ context.Context, _ Request) (Result, error) {
		panic("kaboom")
	})
	h, id, runID, w := claimedHarness(t, exec)
	_ = w.Run(context.Background(), id, runID, SystemActor())
	// Non-retryable panic → task failed (not ready).
	if got := h.getTask(t, id).Status; got != StatusFailed {
		t.Errorf("status=%q want failed after panic", got)
	}
}

func TestWorker_PromotesRunToRunning(t *testing.T) {
	exec := executorFunc(func(_ context.Context, req Request) (Result, error) {
		// The executor receives the run snapshot loaded before promotion, so
		// it sees 'queued'; the DB row is promoted to 'running' first.
		_ = req.Run.Status
		return Result{Action: TerminalSubmit, Output: map[string]any{}}, nil
	})
	h, id, runID, w := claimedHarness(t, exec)
	if err := w.Run(context.Background(), id, runID, SystemActor()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, _ := h.q.GetAgentTaskRun(context.Background(), runID)
	if !run.StartedAt.Valid {
		t.Errorf("run should have been promoted with started_at set")
	}
}
