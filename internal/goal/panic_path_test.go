package goal

import (
	"testing"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TestWorkerPanicPathReleasesTheRun proves that a panicking executor does not
// leave the Run it admitted renewing. The ExecutorResult that would carry the
// handoff is never returned by a panic, so the worker's attempt-scoped ownership
// tracker is the only thing that can release it — without it the Run stays
// `running` and its heartbeat keeps renewing until the lease expires.
func TestWorkerPanicPathReleasesTheRun(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(root.ID)

	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		// A panic means no ExecutorResult is ever returned, so the handoff must be
		// reported out of band. scriptedExecutor does that on the normal path.
		req.OnTurnStarted(fx.admit(ctx, req.Attempt.SessionID))
		panic("executor exploded after admission")
	}

	att, err := h.svc.Claim(ctx, root.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	runErr := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker})
	if runErr == nil {
		t.Fatal("worker Run returned nil for a panicking executor")
	}
	if len(fx.runs) != 1 {
		t.Fatalf("admitted Runs = %d, want 1", len(fx.runs))
	}
	if status := fx.runStatus(ctx, fx.runs[0]); status != agentrun.StatusFailed {
		t.Fatalf("Run status after panic = %q, want failed", status)
	}
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs after panic = %d, want 0 (the panicking turn never released its Run)", live)
	}
	// The panic is recorded as a failed attempt, fenced by the Run the attempt had
	// admitted.
	attempts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: root.ID, Purpose: pgnull.Text(PurposeExecution)})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != AttemptFailed || attempts[0].FailureClass != FailureClassEnvironment {
		t.Fatalf("attempts = %+v, want one environment failure", attempts)
	}
}

// TestWorkerPanicInARepairTurnReleasesThatTurn proves the same for the repair
// iteration. The loop clears its pending handoff before admitting the next turn,
// so when that turn panics the deferred release has nothing of its own; the
// attempt-scoped tracker is what releases the Run the panicking turn admitted.
func TestWorkerPanicInARepairTurnReleasesThatTurn(t *testing.T) {
	h := newHarness(t)
	fx := newGoalRunFixture(t, h)
	ctx := t.Context()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	calls := 0
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		calls++
		switch calls {
		case 1:
			return ExecutorResult{
				Submitted: true, Evidence: AttemptEvidence{Summary: "bad"}, handoff: fx.admit(ctx, req.Attempt.SessionID),
				Decomposition: &DecompositionContent{Children: []ProposedChild{cmp_child("a", false)}},
			}, nil
		case 2:
			// A panic never returns a result, so report the admitted turn out of band
			// exactly as production does.
			req.OnTurnStarted(fx.admit(ctx, req.Attempt.SessionID))
			panic("planner repair exploded")
		default:
			t.Fatalf("unexpected planner call %d", calls)
			return ExecutorResult{}, nil
		}
	}

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if runErr := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker}); runErr == nil {
		t.Fatal("worker Run returned nil for a panicking repair turn")
	}
	if calls != 2 {
		t.Fatalf("planner calls = %d, want 2", calls)
	}
	if len(fx.runs) != 2 {
		t.Fatalf("admitted Runs = %d, want one per planning turn", len(fx.runs))
	}
	// Both turns' Runs are settled: the first by its own iteration, the second by the
	// fallback as its panic unwound the loop.
	for _, runID := range fx.runs {
		if status := fx.runStatus(ctx, runID); status == "running" {
			t.Fatalf("Run %s is still running after a panic in the repair turn", runID)
		}
	}
	if live := fx.store.LocalRuns(); live != 0 {
		t.Fatalf("local Runs after the repair panic = %d, want 0", live)
	}
	attempts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: root.ID, Purpose: pgnull.Text(PurposeDecomposition)})
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != AttemptFailed {
		t.Fatalf("panic must record its failed attempt before releasing the Run: %+v", attempts)
	}
}
