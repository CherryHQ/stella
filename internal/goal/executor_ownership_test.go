package goal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agentrun"
)

func TestTerminalActionPreservesRunOwnership(t *testing.T) {
	handoff := agentruntime.NewExecutionHandoff()
	recorder := &terminalRecorder{}
	if err := recorder.record(Result{Action: terminalSubmit}); err != nil {
		t.Fatal(err)
	}
	events := make(chan agent.Event, 1)
	events <- agent.Event{Text: "submitted"}
	close(events)
	exec := newWorkerExecutor(nil, nil, nil, allowAttempt)
	_, result, done, failure, err := exec.runTurn(t.Context(), executorTurn{
		events: events, cancel: func() {}, handoff: handoff,
	}, recorder)
	if err != nil || failure != nil || !done || result.handoff != handoff {
		t.Fatalf("terminal action lost ownership: done=%v failure=%v error=%v handoff=%p", done, failure, err, result.handoff)
	}
}

// TestRunTurnDrainsAfterAnEventError pins that a turn's stream is consumed to EOF
// even after an event error. The Runtime publishes the turn's execution outcome
// and finishes its own source work before it closes the stream, so returning on
// the first error would let the caller release the Run while producer cleanup was
// still running.
func TestRunTurnDrainsAfterAnEventError(t *testing.T) {
	events := make(chan agent.Event)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		events <- agent.Event{Err: errors.New("runner exploded")}
		// A second event the runtime emits while unwinding. It only lands if the
		// executor keeps reading.
		events <- agent.Event{Text: "cleanup narration"}
		close(events)
	}()

	handoff := agentruntime.NewExecutionHandoff()
	exec := newWorkerExecutor(nil, nil, nil, allowAttempt)
	_, _, done, fail, err := exec.runTurn(t.Context(), executorTurn{
		events: events, cancel: func() {}, handoff: handoff,
	}, &terminalRecorder{})
	if err != nil {
		t.Fatalf("runTurn error = %v, want the failure reported as a result", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("runTurn left the stream producer blocked")
	}
	if done || fail == nil {
		t.Fatalf("done=%v fail=%v, want a failed turn", done, fail)
	}
	if fail.handoff != handoff {
		t.Fatal("the failed turn dropped its execution ownership")
	}
	if fail.Failure == nil || fail.Failure.Reason == "" || fail.Failure.FailureClass == "" {
		t.Fatalf("failed result = %+v, want a classified failure", *fail)
	}
}

// TestWorkerReleasesTheRunWhenTheExecutorIsCanceled pins that an executor error
// return does not lose the Run's ownership: the cancelled turn was admitted, so it
// must be released with the canceled outcome instead of renewing until its lease
// expires. The pre-fix worker returned before installing its release.
func TestWorkerReleasesTheRunWhenTheExecutorIsCanceled(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(root.ID)

	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		handoff := fx.admit(ctx, req.Attempt.SessionID)
		// The turn stops before declaring a terminal action and the executor reports
		// the cancellation as an error, exactly like a shutdown mid-attempt.
		return ExecutorResult{handoff: handoff}, context.Canceled
	}

	att, err := h.svc.Claim(ctx, root.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	runErr := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("worker Run error = %v, want the cancellation", runErr)
	}
	if len(fx.runs) != 1 {
		t.Fatalf("admitted Runs = %d, want 1", len(fx.runs))
	}
	if status := fx.runStatus(ctx, fx.runs[0]); status != agentrun.StatusCanceled {
		t.Fatalf("Run status = %q, want canceled", status)
	}
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs = %d, want 0 (the cancelled turn was never released)", live)
	}
}
