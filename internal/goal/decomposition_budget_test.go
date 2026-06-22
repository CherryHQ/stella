package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
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
		t.Fatalf("after queued reap lifecycle=%q want draft (not blocked — budget refunded)", d.Lifecycle)
	}
	var pol ConvergencePolicy
	_ = unmarshalJSON(d.ConvergencePolicy, &pol)
	if pol.MaxAttempts != defaultMaxAttempts+1 {
		t.Fatalf("after queued reap MaxAttempts=%d want %d (refund raised the ceiling)", pol.MaxAttempts, defaultMaxAttempts+1)
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
	if err := h.svc.FailAttempt(ctx, att.ID, "planner produced no valid decomposition"); err != nil {
		t.Fatalf("FailAttempt: %v", err)
	}
	blocked := h.get(childA)
	if blocked.Lifecycle != LifecycleBlocked || blocked.BlockReason != BlockBudgetExhausted {
		t.Fatalf("child after budget-out lifecycle=%q reason=%q want blocked/budget_exhausted",
			blocked.Lifecycle, blocked.BlockReason)
	}

	// A budget block does not eagerly maintain the parent counter; the parent only
	// learns of the stall through the reconcile backstop.
	if got := h.get(root.ID).RequiredBlocked; got != 0 {
		t.Fatalf("parent required_blocked=%d want 0 before reconcile (budget block is not eager)", got)
	}
	if err := h.svc.reconcileCounters(ctx, root.ID); err != nil {
		t.Fatalf("reconcileCounters: %v", err)
	}
	p := h.get(root.ID)
	if p.RequiredBlocked != 1 {
		t.Fatalf("parent required_blocked=%d want 1 after reconcile", p.RequiredBlocked)
	}
	if v := RollupComposite(p); v != RollupBlock {
		t.Fatalf("parent rollup with 1 blocked required child = %q want block", v)
	}

	// The dispatcher applies that verdict as a dep-block on the parent.
	if err := h.svc.Block(ctx, root.ID, BlockDep, SystemActor()); err != nil {
		t.Fatalf("Block(parent): %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleBlocked {
		t.Fatalf("parent after rollup-block lifecycle=%q want blocked", got)
	}
}
