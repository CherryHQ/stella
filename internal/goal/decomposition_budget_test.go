package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TestCreateGoal_CompositeDeterministicRejected pins CR-001 at the create
// boundary: forcing every root to composite must not accept a deterministic
// acceptance contract — it would have no check event source and stall the root
// active forever. Judgment-only contracts and trivial contracts are fine.
func TestCreateGoal_CompositeDeterministicRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.svc.CreateRoot(ctx, CreateInput{
		UserID: h.userID, AgentID: h.agentID, Title: "root", Intent: "x",
		Kind: KindComposite, Required: true, Contract: detItemContract(),
	})
	if !errors.Is(err, ErrCompositeDeterministicContract) {
		t.Fatalf("composite root with deterministic contract err=%v want ErrCompositeDeterministicContract", err)
	}

	// A judgment-only composite contract is allowed (resolved via SubmitVerdict).
	if _, err := h.svc.CreateRoot(ctx, CreateInput{
		UserID: h.userID, AgentID: h.agentID, Title: "root2", Intent: "x",
		Kind: KindComposite, Required: true, Contract: humanJudgmentContract(),
	}); err != nil {
		t.Fatalf("composite root with judgment contract err=%v want nil", err)
	}
}

// TestDecompositionQueuedReap_RefundsBudget pins CR-002: a queued decomposition
// attempt reaped before River ever ran it (queue backpressure) must NOT charge
// the plan budget. The reap returns the composite to draft and refunds the
// attempt_no it consumed by raising MaxAttempts, so a string of queued reaps
// can't block a composite without a single real planning failure.
func TestDecompositionQueuedReap_RefundsBudget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	root := h.createRoot(KindComposite, AcceptanceContract{}) // zero-value policy ⇒ MaxAttempts default
	noop := AttemptEnqueuer(func(_ context.Context, _ pgx.Tx, _, _ string) error { return nil })

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, noop)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if att.Status != AttemptQueued {
		t.Fatalf("autonomous decomposition attempt status=%q want queued (River not run in tests)", att.Status)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleActive {
		t.Fatalf("after BeginAutoDecomposition lifecycle=%q want active", got)
	}

	if err := h.svc.ReapAttempt(ctx, att.ID); err != nil {
		t.Fatalf("ReapAttempt(queued): %v", err)
	}
	d := h.get(root.ID)
	if d.Lifecycle != LifecycleDraft {
		t.Fatalf("after queued reap lifecycle=%q want draft (not blocked; never-run attempt is not billable)", d.Lifecycle)
	}
	if d.BudgetBonus != 0 {
		t.Fatalf("after queued reap budget_bonus=%d want 0", d.BudgetBonus)
	}
	spent, err := h.svc.spentAttemptBudget(ctx, h.q, d.ID, PurposeDecomposition)
	if err != nil {
		t.Fatalf("count decomposition budget: %v", err)
	}
	if spent != 0 {
		t.Fatalf("after queued reap billable decomposition attempts=%d want 0", spent)
	}
}

// TestCancel_FinalizesUnpointedInflightAttempts pins the root fix for terminal
// resurrection: Cancel must finalize EVERY queued/running attempt on the goal,
// not just the one active_attempt_id points at — decomposition (and review)
// attempts are never pointed there and used to outlive the cancel, to be reaped
// later against a terminal goal.
func TestCancel_FinalizesUnpointedInflightAttempts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	noop := AttemptEnqueuer(func(_ context.Context, _ pgx.Tx, _, _ string) error { return nil })

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, noop)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("promote decomposition: %v", err)
	}
	if err := h.svc.Cancel(ctx, root.ID, "fixture dead", UserActor(h.userID)); err != nil {
		t.Fatalf("cancel mid-decomposition: %v", err)
	}

	a, err := h.q.GetAttempt(ctx, att.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if a.Status != AttemptCancelled {
		t.Fatalf("in-flight decomposition attempt status=%q want cancelled (unpointed attempts must not outlive Cancel)", a.Status)
	}
	// Nothing left for the reaper: a later reap of the finalized attempt is a
	// clean no-op race, never a resurrection route.
	if err := h.svc.ReapAttempt(ctx, att.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reap of finalized attempt err=%v want ErrInvalidTransition", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("lifecycle=%q want cancelled", got)
	}
}

// orphanDecomposition injects a running decomposition attempt directly (bypassing
// Cancel's finalize sweep) onto a goal, simulating an attempt raced into
// existence against a concurrent cancel on another node — the scenario the
// finalization entry points' terminal guards backstop.
func orphanDecomposition(t *testing.T, h *harness, goalID string) sqlc.AgentGoalAttempt {
	t.Helper()
	ctx := context.Background()
	d := h.get(goalID)
	maxNo, err := h.q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{GoalID: goalID, Purpose: PurposeDecomposition})
	if err != nil {
		t.Fatalf("max decomposition no: %v", err)
	}
	sid, err := h.sessionMinter()(ctx, d.UserID, d.AgentID, d.ProjectID.String)
	if err != nil {
		t.Fatalf("mint attempt session: %v", err)
	}
	att, err := h.q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
		ID:              newID(),
		GoalID:          goalID,
		UserID:          d.UserID,
		AgentID:         pgnull.Text(d.AgentID),
		ExecutorAgentID: pgnull.Text(d.AgentID),
		SessionID:       sid,
		Purpose:         PurposeDecomposition,
		AttemptNo:       maxNo + 1,
		Status:          AttemptQueued,
		InputContext:    marshalJSON(AttemptInput{}),
	})
	if err != nil {
		t.Fatalf("create orphan attempt: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("promote orphan attempt: %v", err)
	}
	return att
}

// TestReapAfterCancel_DoesNotResurrect pins the reaper's terminal guard: an
// attempt that escaped Cancel's finalize sweep (cross-node race) must be
// finalized without routing recovery — a live corpse walked the unguarded path
// (cancelled -> reaped flaky decomposition -> ready, where nothing claims a
// composite).
func TestReapAfterCancel_DoesNotResurrect(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	if err := h.svc.Cancel(ctx, root.ID, "fixture dead", UserActor(h.userID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	att := orphanDecomposition(t, h, root.ID)

	if err := h.svc.ReapAttempt(ctx, att.ID); err != nil {
		t.Fatalf("reap after cancel: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("after reap lifecycle=%q want cancelled (terminal must not resurrect)", got)
	}
	a, err := h.q.GetAttempt(ctx, att.ID)
	if err != nil {
		t.Fatalf("get reaped attempt: %v", err)
	}
	if a.Status != AttemptInterrupted {
		t.Fatalf("reaped attempt status=%q want interrupted (finalize must stick)", a.Status)
	}
}

// TestFailAfterCancel_FinalizesWithoutResurrecting pins FailAttempt's terminal
// guard: a worker reporting failure for a goal cancelled mid-run must finalize
// the attempt and leave the goal alone. Without the guard the env/contract
// routes 0-rowed on blockGoal and rolled the finalize back, leaving the attempt
// to be reaped in a loop.
func TestFailAfterCancel_FinalizesWithoutResurrecting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	if err := h.svc.Cancel(ctx, root.ID, "fixture dead", UserActor(h.userID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	att := orphanDecomposition(t, h, root.ID)

	if err := h.svc.FailAttempt(ctx, att.ID, "sandbox gone", FailureClassEnvironment); err != nil {
		t.Fatalf("fail after cancel: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("after fail lifecycle=%q want cancelled (terminal must not resurrect)", got)
	}
	a, err := h.q.GetAttempt(ctx, att.ID)
	if err != nil {
		t.Fatalf("get failed attempt: %v", err)
	}
	if a.Status != AttemptFailed {
		t.Fatalf("attempt status=%q want failed (finalize must not roll back)", a.Status)
	}
}

// TestDecompositionBudgetExhausted_ParentBlocks pins the autonomous-decomposition
// budget path (issue #578) end to end: a composite child whose planner never
// produces a valid decomposition exhausts its plan budget and parks
// blocked(budget_exhausted), and the parent composite eventually surfaces the
// stall as blocked.
//
// The two halves it guards:
//   - recoverDecomposition charges one plan-budget unit per reported decomposition
//     failure; once spent >= MaxAttempts it blockBudgets the composite instead of
//     looping draft→active→draft forever.
//   - a budget block does NOT eagerly bump the parent's required_blocked (only the
//     dep-block path does); the parent learns of the stall through the
//     reconcileCounters backstop, which counts lifecycle='blocked' children. After
//     reconcile the parent's rollup verdict is RollupBlock.
func TestDecompositionBudgetExhausted_ParentBlocks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Parent composite with one required composite child whose plan budget is a
	// single attempt, so one decomposition failure exhausts it.
	root := h.createRoot(KindComposite, AcceptanceContract{})
	content := DecompositionContent{
		Children: []ProposedChild{{
			Key:                "a",
			Title:              "child-a",
			Intent:             "do a",
			Kind:               KindComposite,
			Required:           true,
			AcceptanceContract: AcceptanceContract{},
			ConvergencePolicy:  ConvergencePolicy{MaxAttempts: 1},
			ReviewPolicy:       ReviewNone,
		}},
	}
	cmp_decompose(t, h, root.ID, content)
	childA := childID(root.ID, "a")
	if got := h.get(childA); got.Kind != KindComposite || got.Lifecycle != LifecycleDraft {
		t.Fatalf("materialized child kind=%q lifecycle=%q want composite/draft", got.Kind, got.Lifecycle)
	}

	// Drive the child's single decomposition attempt and report it failed. With
	// MaxAttempts=1 the charge exhausts the plan budget on the first failure, so the
	// child parks blocked(budget_exhausted) instead of returning to draft.
	att, err := h.svc.BeginDecomposition(ctx, childA)
	if err != nil {
		t.Fatalf("BeginDecomposition(child): %v", err)
	}
	if err := h.svc.FailAttempt(ctx, att.ID, "planner produced no valid decomposition", FailureClassModel); err != nil {
		t.Fatalf("FailAttempt: %v", err)
	}
	blocked := h.get(childA)
	if blocked.Lifecycle != LifecycleBlocked || blocked.BlockReason != BlockBudgetExhausted {
		t.Fatalf("child after budget-out lifecycle=%q reason=%q want blocked/budget_exhausted",
			blocked.Lifecycle, blocked.BlockReason)
	}

	tally, err := h.q.GetRequiredChildRollupCounts(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetRequiredChildRollupCounts: %v", err)
	}
	if tally.Blocked != 1 {
		t.Fatalf("blocked tally=%d want 1", tally.Blocked)
	}
	if v := RollupComposite(h.get(root.ID), tally); v != RollupBlock {
		t.Fatalf("parent rollup with 1 blocked required child = %q want block", v)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleActive {
		t.Fatalf("parent lifecycle=%q want active; dep-block plumbing is gone", got)
	}
}
