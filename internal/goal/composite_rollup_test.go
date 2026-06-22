package goal

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// composite_rollup_test.go covers the composite surface end-to-end: decomposition
// (BeginDecomposition -> SubmitDecomposition), the inline plan + plan-approval
// gate, Materialize (children + edges + required_total), the section 6 incremental
// rollup counters, and the composite acceptance gate (trivial auto-accept vs an
// authored judgment contract). It tests to the contract section 2 / 3.7 / 6
// invariants the cited code comments document.
//
// Lifecycle note: the only service path that puts a composite into 'active' (the
// lifecycle every rollup requires) is BeginDecomposition (draft->active). For
// review_policy=none, SubmitDecomposition then materializes the children AND
// releases the leaves to 'ready' in the same flow, so these tests decompose, run
// each ready leaf, and invoke the dispatcher's RollupAccept/RollupFail to apply
// the parent transition. For review_policy=human, SubmitDecomposition instead
// parks the composite at blocked(needs_plan_approval) with the plan stored inline
// on the goal; ApprovePlan materializes (or RejectPlan returns it to draft).

// cmp_child is a single proposed leaf child with a trivial (auto-accept) contract.
func cmp_child(key string, required bool) ProposedChild {
	return ProposedChild{
		Key:                key,
		Title:              "child-" + key,
		Intent:             "do " + key,
		Kind:               KindLeaf,
		Required:           required,
		AcceptanceContract: AcceptanceContract{},
		ConvergencePolicy:  ConvergencePolicy{},
	}
}

// cmp_decompose drives a draft composite through BeginDecomposition ->
// SubmitDecomposition(content). For review_policy=none this materializes the
// children, releases the leaves to ready, and leaves the composite 'active'. For
// review_policy=human it parks the composite at blocked(needs_plan_approval) with
// the plan stored inline. Child ids anchor on the composite id: childID(compositeID, key).
func cmp_decompose(t *testing.T, h *harness, compositeID string, content DecompositionContent) {
	t.Helper()
	ctx := context.Background()
	att, err := h.svc.BeginDecomposition(ctx, compositeID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	if att.Purpose != PurposeDecomposition {
		t.Fatalf("decomposition attempt purpose=%q want %q", att.Purpose, PurposeDecomposition)
	}
	if got := h.get(compositeID).Lifecycle; got != LifecycleActive {
		t.Fatalf("after BeginDecomposition composite lifecycle=%q want active", got)
	}
	// Promote the decomposition attempt queued->running, as the River worker does
	// before it submits the planner's result (SubmitDecomposition needs 'running').
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("PromoteAttempt: %v", err)
	}
	if err := h.svc.SubmitDecomposition(ctx, att.ID, AttemptEvidence{}, content); err != nil {
		t.Fatalf("SubmitDecomposition: %v", err)
	}
}

// cmp_compositeChild is a proposed COMPOSITE child (decomposed in its own turn).
func cmp_compositeChild(key string, required bool) ProposedChild {
	c := cmp_child(key, required)
	c.Kind = KindComposite
	return c
}

// cmp_children returns a composite's direct children ordered by position.
func cmp_children(t *testing.T, h *harness, parentID string) []sqlc.AgentGoal {
	t.Helper()
	kids, err := h.q.ListGoalChildren(context.Background(), pgnull.Text(parentID))
	if err != nil {
		t.Fatalf("ListGoalChildren: %v", err)
	}
	return kids
}

// cmp_acceptChild runs an already-ready leaf child to accepted. The none-path
// SubmitDecomposition releases leaves to 'ready', so no activate is needed.
func cmp_acceptChild(t *testing.T, h *harness, childID string) {
	t.Helper()
	if got := h.get(childID).Lifecycle; got != LifecycleReady {
		t.Fatalf("child %s lifecycle=%q want ready (released by decomposition)", childID, got)
	}
	h.runLeaf(childID)
	if got := h.get(childID).Lifecycle; got != LifecycleAccepted {
		t.Fatalf("child %s after run lifecycle=%q want accepted", childID, got)
	}
}

// TestCompositeRollup_DecomposeMaterializeChildren proves the decomposition ->
// auto-materialize (review_policy=none) path: SubmitDecomposition materializes the
// proposal's children + edges and releases the leaves in one flow (contract
// section 6). Asserts each child exists with parent_id=composite,
// root_id=composite.root_id, depth=parent.depth+1, the right kind/required, the
// materialized edge, and that the composite's required_total counts ONLY the
// required children + the plan gate (planned_at) is set.
func TestCompositeRollup_DecomposeMaterializeChildren(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, AcceptanceContract{})

	content := DecompositionContent{
		Children: []ProposedChild{
			cmp_child("a", true),
			cmp_child("b", true),
			cmp_child("c", false), // advisory: not counted in required_total
		},
		Edges: []ProposedEdge{
			{DownstreamKey: "b", UpstreamKey: "a", Kind: EdgeHard, OnFailure: OnFailureBlock},
		},
	}
	cmp_decompose(t, h, root.ID, content)

	kids := cmp_children(t, h, root.ID)
	if len(kids) != 3 {
		t.Fatalf("materialized %d children want 3", len(kids))
	}
	for i, c := range kids {
		if !c.ParentID.Valid || c.ParentID.String != root.ID {
			t.Errorf("child %s parent_id=%v want %s", c.ID, c.ParentID, root.ID)
		}
		if c.RootID != root.RootID {
			t.Errorf("child %s root_id=%s want %s", c.ID, c.RootID, root.RootID)
		}
		if c.Depth != root.Depth+1 {
			t.Errorf("child %s depth=%d want %d", c.ID, c.Depth, root.Depth+1)
		}
		if c.Position != int64(i) {
			t.Errorf("child %s position=%d want %d", c.ID, c.Position, i)
		}
		// review_policy=none releases leaf children draft->ready in the same flow.
		if c.Lifecycle != LifecycleReady {
			t.Errorf("child %s lifecycle=%q want ready (released by decomposition)", c.ID, c.Lifecycle)
		}
	}

	// required_total counts only the required children (a, b); c is advisory.
	parent := h.get(root.ID)
	if parent.RequiredTotal != 2 {
		t.Fatalf("composite required_total=%d want 2 (required children only)", parent.RequiredTotal)
	}
	if !parent.PlannedAt.Valid {
		t.Fatalf("composite planned_at not set after materialize (plan gate)")
	}

	// The proposed edge b->a was materialized (resolves to deterministic child ids).
	down := childID(root.ID, "b")
	up := childID(root.ID, "a")
	if _, err := h.q.GetEdge(context.Background(), sqlc.GetEdgeParams{GoalID: down, UpstreamID: up}); err != nil {
		t.Fatalf("materialized edge b->a missing: %v", err)
	}
}

// TestCompositeRollup_RequiredAcceptedCounterAndAccept drives both required
// children of a trivial composite to accepted and asserts the section 6 rollup:
// each required child's acceptance bumps the parent's required_accepted by EXACTLY
// 1 (an advisory child does NOT), and once required_accepted == required_total the
// dispatcher's RollupAccept accepts the composite (trivial contract => immediate
// accept on the all-children-accepted rollup).
func TestCompositeRollup_RequiredAcceptedCounterAndAccept(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, AcceptanceContract{})

	content := DecompositionContent{
		Children: []ProposedChild{
			cmp_child("a", true),
			cmp_child("b", true),
			cmp_child("c", false), // advisory
		},
	}
	cmp_decompose(t, h, root.ID, content)

	kids := cmp_children(t, h, root.ID)
	byKey := map[string]sqlc.AgentGoal{}
	for _, c := range kids {
		// child id is deterministic from (goal, key); recover the key by match.
		for _, k := range []string{"a", "b", "c"} {
			if c.ID == childID(root.ID, k) {
				byKey[k] = c
			}
		}
	}
	if len(byKey) != 3 {
		t.Fatalf("could not map all children by key: %v", byKey)
	}

	// Accept the advisory child first: it must NOT bump required_accepted (section 6
	// -- only a required child contributes to the parent's rollup counters).
	cmp_acceptChild(t, h, byKey["c"].ID)
	if got := h.get(root.ID).RequiredAccepted; got != 0 {
		t.Fatalf("after advisory child accept required_accepted=%d want 0 (advisory excluded)", got)
	}

	// First required child: required_accepted bumps to exactly 1; not yet all.
	cmp_acceptChild(t, h, byKey["a"].ID)
	p := h.get(root.ID)
	if p.RequiredAccepted != 1 {
		t.Fatalf("after 1 required child required_accepted=%d want 1", p.RequiredAccepted)
	}
	if v := RollupComposite(p); v != RollupWait {
		t.Fatalf("rollup with 1/2 required accepted = %q want wait", v)
	}
	if p.Lifecycle != LifecycleActive {
		t.Fatalf("composite lifecycle=%q want active (rollup not yet fired)", p.Lifecycle)
	}

	// Second required child: required_accepted == required_total => accept_parent.
	cmp_acceptChild(t, h, byKey["b"].ID)
	p = h.get(root.ID)
	if p.RequiredAccepted != 2 {
		t.Fatalf("after 2 required children required_accepted=%d want 2", p.RequiredAccepted)
	}
	if v := RollupComposite(p); v != RollupAcceptParent {
		t.Fatalf("rollup with 2/2 required accepted = %q want accept_parent", v)
	}

	// The dispatcher applies the verdict: a trivial composite accepts immediately.
	if err := h.svc.RollupAccept(context.Background(), root.ID); err != nil {
		t.Fatalf("RollupAccept: %v", err)
	}
	got := h.get(root.ID)
	if got.Lifecycle != LifecycleAccepted {
		t.Fatalf("composite after RollupAccept lifecycle=%q want accepted", got.Lifecycle)
	}
	if !got.AcceptedOutput.Valid {
		t.Fatalf("accepted composite has no frozen accepted_output")
	}
}

// TestCompositeRollup_RequiredFailedGatesComposite proves the section 6 fail path:
// a required child reaching a terminal-bad state bumps the parent's required_failed
// (precedence over accepted), and RollupComposite => fail drives the composite to
// rejected_final. The terminal-bad here is Cancel (cancelled is terminal-bad and
// bumps the parent required_failed, contract section 2.1 / 6) -- the same counter
// the convergence budget-out rejected_final/abandoned paths bump.
func TestCompositeRollup_RequiredFailedGatesComposite(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, AcceptanceContract{})

	content := DecompositionContent{
		Children: []ProposedChild{
			cmp_child("a", true),
			cmp_child("b", true),
		},
	}
	cmp_decompose(t, h, root.ID, content)

	childA := childID(root.ID, "a")
	childB := childID(root.ID, "b")

	// Accept one required child, terminally fail the other.
	cmp_acceptChild(t, h, childA)
	if got := h.get(root.ID).RequiredAccepted; got != 1 {
		t.Fatalf("required_accepted=%d want 1", got)
	}

	// Cancel the other required child (terminal-bad => parent required_failed +1).
	if err := h.svc.Cancel(context.Background(), childB, "give up", UserActor(h.userID)); err != nil {
		t.Fatalf("Cancel child: %v", err)
	}
	if got := h.get(childB).Lifecycle; got != LifecycleCancelled {
		t.Fatalf("cancelled child lifecycle=%q want cancelled", got)
	}
	p := h.get(root.ID)
	if p.RequiredFailed != 1 {
		t.Fatalf("required_failed=%d want 1 after a required child cancels", p.RequiredFailed)
	}
	// Fail takes precedence over accepted even though required_accepted is 1.
	if v := RollupComposite(p); v != RollupFail {
		t.Fatalf("rollup with 1 failed required child = %q want fail", v)
	}

	if err := h.svc.RollupFail(context.Background(), root.ID); err != nil {
		t.Fatalf("RollupFail: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleRejectedFinal {
		t.Fatalf("composite after RollupFail lifecycle=%q want rejected_final", got)
	}
}

// TestCompositeRollup_AuthoredContractGate proves the composite acceptance gate
// for an AUTHORED (non-trivial) contract: once all required children accept, the
// rollup does NOT auto-accept the composite -- RollupAccept folds the composite's
// own ledger (applyAcceptance). A required human-judgment item is unresolved, so
// the composite blocks(needs_verdict); a human SubmitVerdict(pass) then accepts
// it. A composite carries no executed output, so the evaluated hash is "" and the
// verdict's scope_hash must be "" to match (section 4.2).
func TestCompositeRollup_AuthoredContractGate(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, humanJudgmentContract())

	content := DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	}
	cmp_decompose(t, h, root.ID, content)

	cmp_acceptChild(t, h, childID(root.ID, "a"))
	p := h.get(root.ID)
	if p.RequiredAccepted != p.RequiredTotal || p.RequiredTotal != 1 {
		t.Fatalf("required_accepted=%d required_total=%d want 1/1", p.RequiredAccepted, p.RequiredTotal)
	}
	if v := RollupComposite(p); v != RollupAcceptParent {
		t.Fatalf("rollup all-children-accepted = %q want accept_parent", v)
	}

	// Authored contract: RollupAccept folds the composite's own ledger. The
	// required human-judgment item is pending => block(needs_verdict), NOT accepted.
	if err := h.svc.RollupAccept(context.Background(), root.ID); err != nil {
		t.Fatalf("RollupAccept (authored): %v", err)
	}
	p = h.get(root.ID)
	if p.Lifecycle != LifecycleBlocked || p.BlockReason != BlockNeedsVerdict {
		t.Fatalf("authored composite after RollupAccept lifecycle=%q reason=%q want blocked/needs_verdict",
			p.Lifecycle, p.BlockReason)
	}

	// A human pass-verdict over the (empty-hash) composite output accepts it.
	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID:         root.ID,
		ItemID:         "review",
		Result:         ResultPass,
		ScopeHash:      "", // composite has no executed output => current hash is ""
		ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("SubmitVerdict: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleAccepted {
		t.Fatalf("authored composite after pass verdict lifecycle=%q want accepted", got)
	}
}

// TestCompositeRollup_HumanReviewPlanGate exercises the review_policy=human plan
// gate (contract section 2.3): SubmitDecomposition parks the composite at
// blocked(needs_plan_approval) with the plan stored inline (no children
// materialized yet); ApprovePlan then materializes the children, releases the
// leaves, and returns the composite to active. A second ApprovePlan is rejected
// (the composite is no longer blocked on plan approval -- the materialize fence).
func TestCompositeRollup_HumanReviewPlanGate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	// Switch the composite's review policy to human so SubmitDecomposition gates.
	human := ReviewHuman
	if _, err := h.svc.UpdateMetadata(ctx, root.ID, UpdateInput{ReviewPolicy: &human}); err != nil {
		t.Fatalf("UpdateMetadata review_policy=human: %v", err)
	}

	content := DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true), cmp_child("b", true)},
	}
	cmp_decompose(t, h, root.ID, content)

	// SubmitDecomposition parks the composite for human approval; no children yet.
	p := h.get(root.ID)
	if p.Lifecycle != LifecycleBlocked || p.BlockReason != BlockNeedsPlanApproval {
		t.Fatalf("after human decomposition lifecycle=%q reason=%q want blocked/needs_plan_approval",
			p.Lifecycle, p.BlockReason)
	}
	if p.PlannedAt.Valid {
		t.Fatalf("planned_at set before approval (materialize fence should be open)")
	}
	if kids := cmp_children(t, h, root.ID); len(kids) != 0 {
		t.Fatalf("human plan materialized %d children before approval want 0", len(kids))
	}

	// ApprovePlan materializes and returns the composite to active.
	if err := h.svc.ApprovePlan(ctx, root.ID, UserActor(h.userID)); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	p = h.get(root.ID)
	if p.Lifecycle != LifecycleActive {
		t.Fatalf("composite after ApprovePlan lifecycle=%q want active", p.Lifecycle)
	}
	if !p.PlannedAt.Valid {
		t.Fatalf("planned_at not set after ApprovePlan (plan gate)")
	}
	if kids := cmp_children(t, h, root.ID); len(kids) != 2 {
		t.Fatalf("ApprovePlan materialized %d children want 2", len(kids))
	}
	if p.RequiredTotal != 2 {
		t.Fatalf("composite required_total=%d want 2", p.RequiredTotal)
	}

	// A second ApprovePlan is an invalid transition: the composite is active, not
	// blocked on plan approval (the materialize fence).
	if err := h.svc.ApprovePlan(ctx, root.ID, UserActor(h.userID)); err == nil {
		t.Fatalf("second ApprovePlan should fail (composite no longer blocked on plan approval)")
	}
}

// TestCompositeRollup_RejectPlanReturnsToDraft proves the section 2.3 reject path:
// a human RejectPlan clears the inline plan and returns the composite to 'draft'
// for re-decomposition (no children materialize).
func TestCompositeRollup_RejectPlanReturnsToDraft(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	human := ReviewHuman
	if _, err := h.svc.UpdateMetadata(ctx, root.ID, UpdateInput{ReviewPolicy: &human}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	})
	if got := h.get(root.ID).Lifecycle; got != LifecycleBlocked {
		t.Fatalf("after human decomposition lifecycle=%q want blocked", got)
	}

	if err := h.svc.RejectPlan(ctx, root.ID, "scope wrong", UserActor(h.userID)); err != nil {
		t.Fatalf("RejectPlan: %v", err)
	}

	// No children materialized, composite returns to draft, gate untouched.
	if kids := cmp_children(t, h, root.ID); len(kids) != 0 {
		t.Fatalf("rejected plan materialized %d children want 0", len(kids))
	}
	p := h.get(root.ID)
	if p.Lifecycle != LifecycleDraft {
		t.Fatalf("composite after RejectPlan lifecycle=%q want draft (re-decompose)", p.Lifecycle)
	}
	if p.RequiredTotal != 0 || p.PlannedAt.Valid {
		t.Fatalf("rejected plan must not set required_total/planned_at: total=%d planned=%v",
			p.RequiredTotal, p.PlannedAt.Valid)
	}
}

// TestCompositeRollup_RecoverBlockedParentAfterNestedPlanApproval proves the
// rollup-recovery path (contract section 6): a required COMPOSITE child that parks
// at blocked(needs_plan_approval) drives its parent to blocked(dep) via the
// reconcile backstop. Once the child's plan is approved the child returns to
// active, but the parent is invisible to the active-only rollup scans, so
// RecoverBlockedComposite (run by the dispatcher over ListBlockedDepComposites)
// must wake it back to active and the tree must then converge. Without recovery
// the parent strands in blocked(dep) forever.
func TestCompositeRollup_RecoverBlockedParentAfterNestedPlanApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	// root decomposes (review_policy=none) into one required COMPOSITE child, left
	// in draft for its own decomposition.
	cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_compositeChild("sub", true)},
	})
	subID := childID(root.ID, "sub")
	if got := h.get(subID); got.Kind != KindComposite || got.Lifecycle != LifecycleDraft {
		t.Fatalf("sub kind=%q lifecycle=%q want composite/draft", got.Kind, got.Lifecycle)
	}

	// sub uses human plan review; decomposing it parks it blocked(needs_plan_approval).
	human := ReviewHuman
	if _, err := h.svc.UpdateMetadata(ctx, subID, UpdateInput{ReviewPolicy: &human}); err != nil {
		t.Fatalf("UpdateMetadata sub review_policy=human: %v", err)
	}
	cmp_decompose(t, h, subID, DecompositionContent{
		Children: []ProposedChild{cmp_child("x", true)},
	})
	if got := h.get(subID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsPlanApproval {
		t.Fatalf("sub lifecycle=%q reason=%q want blocked/needs_plan_approval", got.Lifecycle, got.BlockReason)
	}

	// Dispatcher backstop: reconcile observes the blocked child, then the rollup
	// drives root active->blocked(dep).
	if err := h.svc.reconcileCounters(ctx, root.ID); err != nil {
		t.Fatalf("reconcileCounters: %v", err)
	}
	if got := h.get(root.ID).RequiredBlocked; got != 1 {
		t.Fatalf("root required_blocked=%d want 1 after reconcile", got)
	}
	if v := RollupComposite(h.get(root.ID)); v != RollupBlock {
		t.Fatalf("rollup with 1 blocked required child = %q want block", v)
	}
	if err := h.svc.Block(ctx, root.ID, BlockDep, SystemActor()); err != nil {
		t.Fatalf("Block(root, dep): %v", err)
	}
	if got := h.get(root.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockDep {
		t.Fatalf("root lifecycle=%q reason=%q want blocked/dep", got.Lifecycle, got.BlockReason)
	}

	// Approve sub's plan: sub returns to active and materializes its leaf child.
	if err := h.svc.ApprovePlan(ctx, subID, UserActor(h.userID)); err != nil {
		t.Fatalf("ApprovePlan(sub): %v", err)
	}
	if got := h.get(subID).Lifecycle; got != LifecycleActive {
		t.Fatalf("sub after ApprovePlan lifecycle=%q want active", got)
	}

	// root is still blocked(dep) and invisible to the active-only rollup scans; the
	// recovery scan must wake it back to active now its blocking child has cleared.
	if err := h.svc.RecoverBlockedComposite(ctx, root.ID); err != nil {
		t.Fatalf("RecoverBlockedComposite: %v", err)
	}
	rootAfter := h.get(root.ID)
	if rootAfter.Lifecycle != LifecycleActive {
		t.Fatalf("root after recovery lifecycle=%q want active (deadlock if still blocked)", rootAfter.Lifecycle)
	}
	if rootAfter.RequiredBlocked != 0 {
		t.Fatalf("root required_blocked=%d want 0 after recovery", rootAfter.RequiredBlocked)
	}

	// End-to-end: drive sub to accepted, then root must roll up to accepted.
	cmp_acceptChild(t, h, childID(subID, "x"))
	if err := h.svc.RollupAccept(ctx, subID); err != nil {
		t.Fatalf("RollupAccept(sub): %v", err)
	}
	if got := h.get(subID).Lifecycle; got != LifecycleAccepted {
		t.Fatalf("sub lifecycle=%q want accepted", got)
	}
	if err := h.svc.RollupAccept(ctx, root.ID); err != nil {
		t.Fatalf("RollupAccept(root): %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleAccepted {
		t.Fatalf("root lifecycle=%q want accepted (nested tree converged)", got)
	}
}

// TestCompositeRollup_RejectsNoRequiredChild proves the section 6 structural guard:
// a decomposition with zero required children is invalid -- SubmitDecomposition
// rejects it (validateContent -> ValidateDecomposition: >=1 required child) so a
// vacuous plan never reaches materialize.
func TestCompositeRollup_RejectsNoRequiredChild(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	att, err := h.svc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	err = h.svc.SubmitDecomposition(ctx, att.ID, AttemptEvidence{}, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", false)}, // all advisory => 0 required
	})
	if err == nil {
		t.Fatalf("SubmitDecomposition with 0 required children should fail (section 6: >=1 required child)")
	}
}

// TestCompositeRollup_RedecomposeAfterInterrupt proves a re-plan after an
// interrupted decomposition does not collide. The first decomposition leaves a
// (goal, decomposition, attempt_no=1) row; when it is interrupted and the goal
// resets to draft, a second BeginDecomposition must number the new attempt from
// the max existing decomposition attempt -- not attempt_count (which only tracks
// execution) -- or it reuses attempt_no=1 and trips uniq_agent_goal_attempt_no.
func TestCompositeRollup_RedecomposeAfterInterrupt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	att1, err := h.svc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("first BeginDecomposition: %v", err)
	}
	if att1.AttemptNo != 1 {
		t.Fatalf("first decomposition attempt_no=%d want 1", att1.AttemptNo)
	}

	// Simulate an interrupted decomposition (worker crash / restart): finalize the
	// attempt out of queued/running and reset the goal active->draft for a re-plan.
	if _, err := h.q.FinalizeAttempt(ctx, sqlc.FinalizeAttemptParams{
		ToStatus: AttemptInterrupted,
		ID:       att1.ID,
	}); err != nil {
		t.Fatalf("FinalizeAttempt: %v", err)
	}
	if _, err := h.q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   LifecycleDraft,
		ID:            root.ID,
		FromLifecycle: LifecycleActive,
	}); err != nil {
		t.Fatalf("reset to draft: %v", err)
	}

	att2, err := h.svc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("re-plan BeginDecomposition: %v", err)
	}
	if att2.AttemptNo != 2 {
		t.Fatalf("re-plan decomposition attempt_no=%d want 2", att2.AttemptNo)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleActive {
		t.Fatalf("after re-plan composite lifecycle=%q want active", got)
	}
}
