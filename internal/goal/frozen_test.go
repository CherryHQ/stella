package goal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// buildSourceTree decomposes a fresh composite root into a leaf 'a' and a
// composite 'sub' (edge sub<-a), then decomposes 'sub' into leaves x,y (edge
// y<-x). It returns the root id. The tree is not driven to accepted: snapshot
// reads the stored plans, which decomposition already persisted.
func buildSourceTree(t *testing.T, h *harness) string {
	t.Helper()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{
			cmp_child("a", true),
			cmp_compositeChild("sub", true),
		},
		Edges: []ProposedEdge{
			{DownstreamKey: "sub", UpstreamKey: "a", Kind: EdgeHard, OnFailure: OnFailureBlock},
		},
	})
	subID := childID(root.ID, "sub")
	cmp_decompose(t, h, subID, DecompositionContent{
		Children: []ProposedChild{
			cmp_child("x", true),
			cmp_child("y", true),
		},
		Edges: []ProposedEdge{
			{DownstreamKey: "y", UpstreamKey: "x", Kind: EdgeHard, OnFailure: OnFailureBlock},
		},
	})
	return root.ID
}

// TestFrozen_SnapshotShape proves SnapshotFrozenPlan reads the stored plans
// recursively: a leaf has a nil sub-plan, a composite carries its own frozen
// sub-plan, and edges are kept verbatim at each level.
func TestFrozen_SnapshotShape(t *testing.T) {
	h := newHarness(t)
	rootID := buildSourceTree(t, h)

	plan, err := h.svc.SnapshotFrozenPlan(context.Background(), rootID)
	if err != nil {
		t.Fatalf("SnapshotFrozenPlan: %v", err)
	}
	if len(plan.Children) != 2 {
		t.Fatalf("root frozen children=%d want 2", len(plan.Children))
	}
	if len(plan.Edges) != 1 || plan.Edges[0].DownstreamKey != "sub" || plan.Edges[0].UpstreamKey != "a" {
		t.Fatalf("root frozen edges=%v want sub<-a", plan.Edges)
	}
	// child 'a' is a leaf: no sub-plan.
	if plan.Children[0].Child.Key != "a" || plan.Children[0].Plan != nil {
		t.Fatalf("child a frozen node=%+v want leaf with nil plan", plan.Children[0])
	}
	// child 'sub' is a composite: carries its own frozen plan with x,y + edge y<-x.
	sub := plan.Children[1]
	if sub.Child.Key != "sub" || sub.Plan == nil {
		t.Fatalf("child sub frozen node=%+v want composite with sub-plan", sub)
	}
	if len(sub.Plan.Children) != 2 || len(sub.Plan.Edges) != 1 {
		t.Fatalf("sub frozen plan children=%d edges=%d want 2/1", len(sub.Plan.Children), len(sub.Plan.Edges))
	}
}

// TestFrozen_InstantiateRoundTrip proves InstantiateFrozen materializes the whole
// tree without the planner: the root is an active composite, leaves are ready,
// the composite child is active+planned, edges are materialized, the root context
// is preserved, and re-snapshotting the new tree reproduces the same FrozenPlan.
func TestFrozen_InstantiateRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rootID := buildSourceTree(t, h)
	plan, err := h.svc.SnapshotFrozenPlan(ctx, rootID)
	if err != nil {
		t.Fatalf("SnapshotFrozenPlan: %v", err)
	}

	rootCtx := json.RawMessage(`{"workflow_id":"wf-1"}`)
	newRoot, err := h.svc.InstantiateFrozen(ctx, FrozenRootSpec{
		UserID:  h.userID,
		AgentID: h.agentID,
		Title:   "instantiated",
		Intent:  "run it",
		Context: rootCtx,
	}, plan)
	if err != nil {
		t.Fatalf("InstantiateFrozen: %v", err)
	}
	if newRoot.ID == rootID {
		t.Fatal("instantiated root reused the source id")
	}
	if newRoot.Kind != KindComposite || newRoot.Lifecycle != LifecycleActive {
		t.Fatalf("new root kind=%q lifecycle=%q want composite/active", newRoot.Kind, newRoot.Lifecycle)
	}
	if !newRoot.PlannedAt.Valid {
		t.Fatal("new root not marked planned (materialize fence)")
	}
	// jsonb re-serializes (whitespace), so compare semantically.
	var gotCtx map[string]any
	if err := json.Unmarshal(newRoot.Context, &gotCtx); err != nil || gotCtx["workflow_id"] != "wf-1" {
		t.Fatalf("new root context=%s want workflow_id=wf-1 (err=%v)", newRoot.Context, err)
	}

	// Leaf 'a' ready; composite 'sub' active+planned.
	a := h.get(childID(newRoot.ID, "a"))
	if a.Kind != KindLeaf || a.Lifecycle != LifecycleReady {
		t.Fatalf("child a kind=%q lifecycle=%q want leaf/ready", a.Kind, a.Lifecycle)
	}
	sub := h.get(childID(newRoot.ID, "sub"))
	if sub.Kind != KindComposite || sub.Lifecycle != LifecycleActive || !sub.PlannedAt.Valid {
		t.Fatalf("child sub kind=%q lifecycle=%q planned=%v want composite/active/planned", sub.Kind, sub.Lifecycle, sub.PlannedAt.Valid)
	}
	// sub's leaves are ready.
	for _, k := range []string{"x", "y"} {
		c := h.get(childID(sub.ID, k))
		if c.Lifecycle != LifecycleReady {
			t.Fatalf("sub child %s lifecycle=%q want ready", k, c.Lifecycle)
		}
	}
	// Edge sub<-a materialized under the new root.
	if _, err := h.q.GetEdge(ctx, sqlc.GetEdgeParams{GoalID: childID(newRoot.ID, "sub"), UpstreamID: childID(newRoot.ID, "a")}); err != nil {
		t.Fatalf("root edge sub<-a not materialized: %v", err)
	}

	// Round-trip: re-snapshot the instantiated tree reproduces the plan.
	got, err := h.svc.SnapshotFrozenPlan(ctx, newRoot.ID)
	if err != nil {
		t.Fatalf("re-SnapshotFrozenPlan: %v", err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, plan)
	}
}

// TestFrozen_SemiFrozenCompositeLeftDraft proves a composite child with a nil
// sub-plan is left in draft for the planner (the semi-frozen case) rather than
// materialized.
func TestFrozen_SemiFrozenCompositeLeftDraft(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	plan := FrozenPlan{
		Children: []FrozenNode{
			{Child: cmp_child("a", true)},
			{Child: cmp_compositeChild("planme", true)}, // Plan nil => planner replan
		},
	}
	root, err := h.svc.InstantiateFrozen(ctx, FrozenRootSpec{UserID: h.userID, AgentID: h.agentID, Intent: "x"}, plan)
	if err != nil {
		t.Fatalf("InstantiateFrozen: %v", err)
	}
	if got := h.get(childID(root.ID, "a")).Lifecycle; got != LifecycleReady {
		t.Fatalf("leaf a lifecycle=%q want ready", got)
	}
	planme := h.get(childID(root.ID, "planme"))
	if planme.Lifecycle != LifecycleDraft || planme.PlannedAt.Valid {
		t.Fatalf("semi-frozen composite lifecycle=%q planned=%v want draft/unplanned", planme.Lifecycle, planme.PlannedAt.Valid)
	}
}

// TestFrozen_ValidateRejectsCompositeDeterministic proves a frozen plan with a
// deterministic acceptance item on a composite child is rejected (the fold would
// stall forever — no executed output to check).
func TestFrozen_ValidateRejectsCompositeDeterministic(t *testing.T) {
	det := AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items:  []AcceptanceItem{{ID: "c", Kind: ItemDeterministic, Required: true, Command: "true"}},
	}
	bad := cmp_compositeChild("sub", true)
	bad.AcceptanceContract = det
	plan := FrozenPlan{Children: []FrozenNode{{Child: bad, Plan: &FrozenPlan{Children: []FrozenNode{{Child: cmp_child("x", true)}}}}}}
	if err := ValidateFrozenPlan(plan, 0, defaultMaxDepth); !errors.Is(err, ErrCompositeDeterministicContract) {
		t.Fatalf("ValidateFrozenPlan err=%v want ErrCompositeDeterministicContract", err)
	}
}
