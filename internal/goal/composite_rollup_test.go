package goal

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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

func cmp_decompose(t *testing.T, h *harness, compositeID string, content DecompositionContent) {
	t.Helper()
	ctx := context.Background()
	att, err := h.svc.BeginDecomposition(ctx, compositeID)
	if err != nil {
		t.Fatalf("BeginDecomposition: %v", err)
	}
	if got := h.get(compositeID).Lifecycle; got != LifecycleActive {
		t.Fatalf("after BeginDecomposition lifecycle=%q want active", got)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("PromoteAttempt: %v", err)
	}
	if err := h.svc.SubmitDecomposition(ctx, att.ID, AttemptEvidence{}, content); err != nil {
		t.Fatalf("SubmitDecomposition: %v", err)
	}
}

func cmp_children(t *testing.T, h *harness, parentID string) []sqlc.AgentGoal {
	t.Helper()
	kids, err := h.q.ListGoalChildren(context.Background(), pgnull.Text(parentID))
	if err != nil {
		t.Fatalf("ListGoalChildren: %v", err)
	}
	return kids
}

func cmp_applyRollup(t *testing.T, h *harness, goalID string) {
	t.Helper()
	ctx := context.Background()
	parent := h.get(goalID)
	tally, err := h.q.GetRequiredChildRollupCounts(ctx, goalID)
	if err != nil {
		t.Fatalf("GetRequiredChildRollupCounts: %v", err)
	}
	switch RollupComposite(parent, tally) {
	case RollupAcceptParent:
		if err := h.svc.RollupAccept(ctx, goalID); err != nil {
			t.Fatalf("RollupAccept: %v", err)
		}
	case RollupFail:
		if err := h.svc.RollupFail(ctx, goalID); err != nil {
			t.Fatalf("RollupFail: %v", err)
		}
	case RollupBlock, RollupWait:
		return
	}
}

func cmp_acceptChild(t *testing.T, h *harness, childID string) {
	t.Helper()
	if got := h.get(childID).Lifecycle; got != LifecyclePending {
		t.Fatalf("child %s lifecycle=%q want pending", childID, got)
	}
	h.runLeaf(childID)
	got := h.get(childID)
	if got.Lifecycle != LifecycleDone || got.DoneReason != DoneReasonAccepted {
		t.Fatalf("child %s done=%q/%q want done/accepted", childID, got.Lifecycle, got.DoneReason)
	}
}

func TestCompositeRollup_DecomposeMaterializeChildren(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, AcceptanceContract{})
	content := DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true), cmp_child("b", true), cmp_child("c", false)},
		Edges:    []ProposedEdge{{DownstreamKey: "b", UpstreamKey: "a", Kind: EdgeHard, OnFailure: OnFailureBlock}},
	}
	cmp_decompose(t, h, root.ID, content)

	parent := h.get(root.ID)
	if parent.Lifecycle != LifecycleActive || !parent.PlannedAt.Valid {
		t.Fatalf("parent lifecycle/planned=%q/%v want active/planned", parent.Lifecycle, parent.PlannedAt.Valid)
	}
	kids := cmp_children(t, h, root.ID)
	if len(kids) != 3 {
		t.Fatalf("children=%d want 3", len(kids))
	}
	tally, err := h.q.GetRequiredChildRollupCounts(context.Background(), root.ID)
	if err != nil {
		t.Fatalf("GetRequiredChildRollupCounts: %v", err)
	}
	if tally.Total != 2 || tally.Accepted != 0 || tally.Failed != 0 || tally.Blocked != 0 || tally.DepFailed != 0 {
		t.Fatalf("tally=%+v want total=2 only", tally)
	}
}

func TestCompositeRollup_AllRequiredAcceptedAcceptsParent(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindComposite, AcceptanceContract{})
	cmp_decompose(t, h, root.ID, DecompositionContent{Children: []ProposedChild{cmp_child("a", true), cmp_child("b", true)}})
	for _, kid := range cmp_children(t, h, root.ID) {
		cmp_acceptChild(t, h, kid.ID)
	}
	cmp_applyRollup(t, h, root.ID)
	parent := h.get(root.ID)
	if parent.Lifecycle != LifecycleDone || parent.DoneReason != DoneReasonAccepted {
		t.Fatalf("parent done=%q/%q want done/accepted", parent.Lifecycle, parent.DoneReason)
	}
}

func TestCompositeRollup_DerivedFailedChildFailsParent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	cmp_decompose(t, h, root.ID, DecompositionContent{Children: []ProposedChild{cmp_child("a", true), cmp_child("b", true)}})
	kids := cmp_children(t, h, root.ID)
	cmp_acceptChild(t, h, kids[0].ID)
	if err := h.svc.Cancel(ctx, kids[1].ID, "test", SystemActor()); err != nil {
		t.Fatalf("Cancel child: %v", err)
	}
	tally, err := h.q.GetRequiredChildRollupCounts(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetRequiredChildRollupCounts: %v", err)
	}
	if tally.Failed != 1 {
		t.Fatalf("failed tally=%d want 1", tally.Failed)
	}
	cmp_applyRollup(t, h, root.ID)
	parent := h.get(root.ID)
	if parent.Lifecycle != LifecycleDone || parent.DoneReason != DoneReasonFailed {
		t.Fatalf("parent done=%q/%q want done/failed", parent.Lifecycle, parent.DoneReason)
	}
}

func TestCompositeRollup_DerivedDependencyDeathFailsParent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("up", true), cmp_child("down", true)},
		Edges:    []ProposedEdge{{DownstreamKey: "down", UpstreamKey: "up", Kind: EdgeHard, OnFailure: OnFailureFail}},
	})
	kids := cmp_children(t, h, root.ID)
	if err := h.svc.Cancel(ctx, kids[0].ID, "test", SystemActor()); err != nil {
		t.Fatalf("Cancel upstream: %v", err)
	}
	tally, err := h.q.GetRequiredChildRollupCounts(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetRequiredChildRollupCounts: %v", err)
	}
	if tally.DepFailed != 1 {
		t.Fatalf("dep_failed tally=%d want 1", tally.DepFailed)
	}
	cmp_applyRollup(t, h, root.ID)
	if got := h.get(root.ID); got.Lifecycle != LifecycleDone || got.DoneReason != DoneReasonFailed {
		t.Fatalf("parent done=%q/%q want done/failed", got.Lifecycle, got.DoneReason)
	}
}

func TestCompositeRollup_HumanReviewPlanGate(t *testing.T) {
	h := newHarness(t)
	root, err := h.svc.CreateRoot(context.Background(), CreateInput{UserID: h.userID, AgentID: h.agentID, Title: "root", Intent: "test", Kind: KindComposite, Required: true, Contract: AcceptanceContract{}, ReviewPolicy: ReviewHuman})
	if err != nil {
		t.Fatalf("CreateRoot: %v", err)
	}
	cmp_decompose(t, h, root.ID, DecompositionContent{Children: []ProposedChild{cmp_child("a", true)}})
	if got := h.get(root.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsPlanApproval {
		t.Fatalf("after submit lifecycle/block=%q/%q want blocked/needs_plan_approval", got.Lifecycle, got.BlockReason)
	}
	if err := h.svc.ApprovePlan(context.Background(), root.ID, UserActor(h.userID)); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	if got := h.get(root.ID); got.Lifecycle != LifecycleActive || !got.PlannedAt.Valid {
		t.Fatalf("after approve lifecycle/planned=%q/%v want active/planned", got.Lifecycle, got.PlannedAt.Valid)
	}
}
