package goal

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// composite_rollup_test.go covers the composite surface end-to-end: decomposition
// (BeginDecomposition → CreateRevision), the revision review FSM, Materialize
// (children + edges + required_total), the §6 incremental rollup counters, and
// the composite acceptance gate (trivial auto-accept vs an authored judgment
// contract). It tests to the contract §2/§3.7/§6 invariants the cited code
// comments document.
//
// Lifecycle note (contract §2, goal-model.md lines 136/186): the ONLY
// service path that puts a composite into 'active' — the lifecycle every rollup
// (RollupComposite, RollupAccept, RollupFail) and the ListRollupCandidates query
// require — is BeginDecomposition (draft→active). After Accept materializes the
// children the composite stays 'active', so these tests drive composites through
// BeginDecomposition first, then ready each draft leaf child individually
// (Activate on a leaf is the plan gate, independent of the parent), run it, and
// invoke the dispatcher's RollupAccept/RollupFail to apply the parent transition.
//
// SUSPECT BUG: the composite lifecycle has no integrated happy path. The contract
// (goal-model.md line 186) says the plan gate is "ready → active requires
// ... an accepted decomposition with ≥1 materialized child", and Activate
// (service.go) implements draft→ready for a composite ONLY when it already has a
// materialized revision (AcceptedRevisionID set + required_total≥1) and flips its
// draft children → ready. But the ONLY route to a materialized revision is
// BeginDecomposition, which moves the composite draft→ACTIVE; once active, Activate
// (guarded from 'draft') can never run, so the materialized children are never
// readied through any service call and a leaf under a decomposed composite has no
// path to 'ready'/claim. Conversely no service call ever moves a composite
// ready→active, so a composite Activated while still draft (if one could
// materialize without BeginDecomposition) ends 'ready' and the rollup — which
// requires 'active' (rollup.go §6, ListRollupCandidates) — never fires. These
// tests sidestep the gap by readying each leaf child individually and invoking
// RollupAccept/RollupFail directly; an integrator should reconcile the draft→
// active vs draft→ready composite transition before relying on the dispatcher.

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

// cmp_decompose drives a draft composite through BeginDecomposition →
// CreateRevision(content) and returns the draft revision. The composite is left
// 'active' (the post-decomposition lifecycle every rollup requires).
func cmp_decompose(t *testing.T, h *harness, compositeID string, content DecompositionContent) sqlc.AgentGoalRevision {
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
	rev, err := h.svc.CreateRevision(ctx, compositeID, content, att.ID)
	if err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}
	if rev.Status != RevisionDraft {
		t.Fatalf("new revision status=%q want draft", rev.Status)
	}
	return rev
}

// cmp_children returns a composite's direct children ordered by position.
func cmp_children(t *testing.T, h *harness, parentID string) []sqlc.AgentGoal {
	t.Helper()
	kids, err := h.q.ListGoalChildren(context.Background(), nullStr(parentID))
	if err != nil {
		t.Fatalf("ListGoalChildren: %v", err)
	}
	return kids
}

// cmp_acceptChild readies a draft leaf child (plan gate) and runs it to accepted.
func cmp_acceptChild(t *testing.T, h *harness, childID string) {
	t.Helper()
	h.activate(childID)
	if got := h.get(childID).Lifecycle; got != LifecycleReady {
		t.Fatalf("child %s after activate lifecycle=%q want ready", childID, got)
	}
	h.runLeaf(childID)
	if got := h.get(childID).Lifecycle; got != LifecycleAccepted {
		t.Fatalf("child %s after run lifecycle=%q want accepted", childID, got)
	}
}

// TestCompositeRollup_DecomposeMaterializeChildren proves the decomposition →
// auto-accept (review_policy=none) → materialize path: Accept materializes the
// proposal's children + edges in one tx (contract §6). Asserts each child exists
// with parent_id=composite, root_id=composite.root_id, depth=parent.depth+1, the
// right kind/required, the materialized edge, and that the composite's
// required_total counts ONLY the required children + points at the accepted
// revision.
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
	rev := cmp_decompose(t, h, root.ID, content)

	// review_policy=none ⇒ Accept auto-accepts draft→accepted and materializes.
	accepted, err := h.svc.Accept(context.Background(), rev.ID, UserActor(h.userID))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.Status != RevisionAccepted {
		t.Fatalf("revision status=%q want accepted", accepted.Status)
	}
	if !accepted.MaterializedAt.Valid {
		t.Fatalf("accepted revision missing materialized_at fence")
	}

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
		if c.Lifecycle != LifecycleDraft {
			t.Errorf("child %s lifecycle=%q want draft (children born draft)", c.ID, c.Lifecycle)
		}
	}

	// required_total counts only the required children (a, b); c is advisory.
	parent := h.get(root.ID)
	if parent.RequiredTotal != 2 {
		t.Fatalf("composite required_total=%d want 2 (required children only)", parent.RequiredTotal)
	}
	if !parent.AcceptedRevisionID.Valid || parent.AcceptedRevisionID.String != rev.ID {
		t.Fatalf("composite accepted_revision_id=%v want %s", parent.AcceptedRevisionID, rev.ID)
	}

	// The proposed edge b→a was materialized (resolves to deterministic child ids).
	down := childID(rev.ID, "b")
	up := childID(rev.ID, "a")
	if _, err := h.q.GetEdge(context.Background(), sqlc.GetEdgeParams{GoalID: down, UpstreamID: up}); err != nil {
		t.Fatalf("materialized edge b→a missing: %v", err)
	}
}

// TestCompositeRollup_RequiredAcceptedCounterAndAccept drives both required
// children of a trivial composite to accepted and asserts the §6 rollup: each
// required child's acceptance bumps the parent's required_accepted by EXACTLY 1
// (an advisory child does NOT), and once required_accepted == required_total the
// dispatcher's RollupAccept accepts the composite (trivial contract ⇒ immediate
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
	rev := cmp_decompose(t, h, root.ID, content)
	if _, err := h.svc.Accept(context.Background(), rev.ID, UserActor(h.userID)); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	kids := cmp_children(t, h, root.ID)
	byKey := map[string]sqlc.AgentGoal{}
	for _, c := range kids {
		// child id is deterministic from (revision, key); recover the key by match.
		for _, k := range []string{"a", "b", "c"} {
			if c.ID == childID(rev.ID, k) {
				byKey[k] = c
			}
		}
	}
	if len(byKey) != 3 {
		t.Fatalf("could not map all children by key: %v", byKey)
	}

	// Accept the advisory child first: it must NOT bump required_accepted (§6 —
	// only a required child contributes to the parent's rollup counters).
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

	// Second required child: required_accepted == required_total ⇒ accept_parent.
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
	if !got.AcceptedOutput.Valid || got.AcceptedOutput.String == "" {
		t.Fatalf("accepted composite has no frozen accepted_output")
	}
}

// TestCompositeRollup_RequiredFailedGatesComposite proves the §6 fail path: a
// required child reaching a terminal-bad state bumps the parent's required_failed
// (precedence over accepted), and RollupComposite ⇒ fail drives the composite to
// rejected_final. The terminal-bad here is Cancel (cancelled is terminal-bad and
// bumps the parent required_failed, contract §2.1/§6) — the same counter the
// convergence budget-out rejected_final/abandoned paths bump.
func TestCompositeRollup_RequiredFailedGatesComposite(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, AcceptanceContract{})

	content := DecompositionContent{
		Children: []ProposedChild{
			cmp_child("a", true),
			cmp_child("b", true),
		},
	}
	rev := cmp_decompose(t, h, root.ID, content)
	if _, err := h.svc.Accept(context.Background(), rev.ID, UserActor(h.userID)); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	childA := childID(rev.ID, "a")
	childB := childID(rev.ID, "b")

	// Accept one required child, terminally fail the other.
	cmp_acceptChild(t, h, childA)
	if got := h.get(root.ID).RequiredAccepted; got != 1 {
		t.Fatalf("required_accepted=%d want 1", got)
	}

	// Cancel the other required child (terminal-bad ⇒ parent required_failed +1).
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
// rollup does NOT auto-accept the composite — RollupAccept folds the composite's
// own ledger (applyAcceptance). A required human-judgment item is unresolved, so
// the composite blocks(needs_verdict); a human SubmitVerdict(pass) then accepts
// it. A composite carries no executed output, so the evaluated hash is "" and the
// verdict's scope_hash must be "" to match (§4.2).
func TestCompositeRollup_AuthoredContractGate(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, humanJudgmentContract())

	content := DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	}
	rev := cmp_decompose(t, h, root.ID, content)
	if _, err := h.svc.Accept(context.Background(), rev.ID, UserActor(h.userID)); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	cmp_acceptChild(t, h, childID(rev.ID, "a"))
	p := h.get(root.ID)
	if p.RequiredAccepted != p.RequiredTotal || p.RequiredTotal != 1 {
		t.Fatalf("required_accepted=%d required_total=%d want 1/1", p.RequiredAccepted, p.RequiredTotal)
	}
	if v := RollupComposite(p); v != RollupAcceptParent {
		t.Fatalf("rollup all-children-accepted = %q want accept_parent", v)
	}

	// Authored contract: RollupAccept folds the composite's own ledger. The
	// required human-judgment item is pending ⇒ block(needs_verdict), NOT accepted.
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
		ScopeHash:      "", // composite has no executed output ⇒ current hash is ""
		ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("SubmitVerdict: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleAccepted {
		t.Fatalf("authored composite after pass verdict lifecycle=%q want accepted", got)
	}
}

// TestCompositeRollup_HumanReviewLifecycle exercises the revision review FSM
// (contract §2.3) for review_policy=human: draft → (SubmitForReview) in_review →
// (Approve) accepted, and that Materialize is gated on 'accepted'. It asserts each
// status transition and that MaterializeRevision rejects a not-yet-accepted
// revision (the accepted-only gate). Approve materializes; the children appear.
func TestCompositeRollup_HumanReviewLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	// Switch the composite's review policy to human so revisions inherit it.
	human := ReviewHuman
	if _, err := h.svc.UpdateMetadata(ctx, root.ID, UpdateInput{ReviewPolicy: &human}); err != nil {
		t.Fatalf("UpdateMetadata review_policy=human: %v", err)
	}

	content := DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true), cmp_child("b", true)},
	}
	rev := cmp_decompose(t, h, root.ID, content)
	if rev.ReviewPolicy != ReviewHuman {
		t.Fatalf("revision review_policy=%q want human (inherited from composite)", rev.ReviewPolicy)
	}

	// Materialize is gated on 'accepted': a draft revision is rejected.
	if _, err := h.bundle.MaterializeRevision(ctx, rev.ID); err == nil {
		t.Fatalf("MaterializeRevision on a draft revision should fail (accepted-only gate)")
	}

	// draft → in_review.
	r, err := h.svc.SubmitForReview(ctx, rev.ID)
	if err != nil {
		t.Fatalf("SubmitForReview: %v", err)
	}
	if r.Status != RevisionInReview {
		t.Fatalf("after SubmitForReview status=%q want in_review", r.Status)
	}

	// in_review → accepted (+ materialize in the same flow).
	r, err = h.svc.Approve(ctx, rev.ID, UserActor(h.userID))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if r.Status != RevisionAccepted {
		t.Fatalf("after Approve status=%q want accepted", r.Status)
	}
	if !r.MaterializedAt.Valid {
		t.Fatalf("approved revision missing materialized_at")
	}

	if kids := cmp_children(t, h, root.ID); len(kids) != 2 {
		t.Fatalf("approve materialized %d children want 2", len(kids))
	}
	if got := h.get(root.ID).RequiredTotal; got != 2 {
		t.Fatalf("composite required_total=%d want 2", got)
	}

	// A re-materialize of the already-materialized revision is an idempotent no-op
	// (the materialized_at fence): children count is unchanged.
	if _, err := h.bundle.MaterializeRevision(ctx, rev.ID); err != nil {
		t.Fatalf("idempotent re-MaterializeRevision: %v", err)
	}
	if kids := cmp_children(t, h, root.ID); len(kids) != 2 {
		t.Fatalf("re-materialize changed child count to %d want 2 (idempotent)", len(kids))
	}
}

// TestCompositeRollup_RejectRevisionStaysActive proves the §2.3 reject path: a
// human Reject moves an in_review revision → rejected and leaves the composite
// 'active' (rework — a fresh decomposition is expected, no children materialize).
// RequestChanges returns it to draft instead. Both are asserted here.
func TestCompositeRollup_RejectRevisionStaysActive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	human := ReviewHuman
	if _, err := h.svc.UpdateMetadata(ctx, root.ID, UpdateInput{ReviewPolicy: &human}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	rev := cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	})

	// draft → in_review → request_changes → draft.
	if _, err := h.svc.SubmitForReview(ctx, rev.ID); err != nil {
		t.Fatalf("SubmitForReview: %v", err)
	}
	back, err := h.svc.RequestChanges(ctx, rev.ID, "tighten the plan", UserActor(h.userID))
	if err != nil {
		t.Fatalf("RequestChanges: %v", err)
	}
	if back.Status != RevisionDraft {
		t.Fatalf("after RequestChanges status=%q want draft", back.Status)
	}

	// draft → in_review → reject.
	if _, err := h.svc.SubmitForReview(ctx, rev.ID); err != nil {
		t.Fatalf("re-SubmitForReview: %v", err)
	}
	rejected, err := h.svc.Reject(ctx, rev.ID, "scope wrong", UserActor(h.userID))
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.Status != RevisionRejected {
		t.Fatalf("after Reject status=%q want rejected", rejected.Status)
	}

	// No children materialized, composite stays active, required_total untouched.
	if kids := cmp_children(t, h, root.ID); len(kids) != 0 {
		t.Fatalf("rejected revision materialized %d children want 0", len(kids))
	}
	p := h.get(root.ID)
	if p.Lifecycle != LifecycleActive {
		t.Fatalf("composite after Reject lifecycle=%q want active (rework)", p.Lifecycle)
	}
	if p.RequiredTotal != 0 || (p.AcceptedRevisionID.Valid && p.AcceptedRevisionID.String != "") {
		t.Fatalf("rejected revision must not set required_total/accepted_revision_id: total=%d rev=%v",
			p.RequiredTotal, p.AcceptedRevisionID)
	}
}

// TestCompositeRollup_RejectsNoRequiredChild proves the §6 structural guard: a
// decomposition with zero required children is invalid — CreateRevision rejects it
// (validateContent → ValidateDecomposition: ≥1 required child) so a vacuous plan
// never reaches materialize.
func TestCompositeRollup_RejectsNoRequiredChild(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	if _, err := h.svc.BeginDecomposition(ctx, root.ID); err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	_, err := h.svc.CreateRevision(ctx, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", false)}, // all advisory ⇒ 0 required
	}, "")
	if err == nil {
		t.Fatalf("CreateRevision with 0 required children should fail (§6: ≥1 required child)")
	}
}

// TestCompositeRollup_RedecomposeAfterInterrupt proves a re-plan after an
// interrupted decomposition does not collide. The first decomposition leaves a
// (goal, decomposition, attempt_no=1) row; when it is interrupted and the goal
// resets to draft, a second BeginDecomposition must number the new attempt from
// the max existing decomposition attempt — not attempt_count (which only tracks
// execution) — or it reuses attempt_no=1 and trips uniq_agent_goal_attempt_no.
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
	// attempt out of queued/running and reset the goal active→draft for a re-plan.
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
