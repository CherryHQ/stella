package goal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// edges_readiness_test.go covers the dependency-edge + readiness-gating surface:
// AddEdge (kind/on_failure persistence + cycle rejection), the pure Compute
// readiness fold over upstream state (readiness.go §), and the WaiveEdge escape
// hatch that unblocks a parked downstream. The pure-fold assertions construct
// edge rows directly; the service-path assertions (AddEdge/WaiveEdge/cycle) wire
// two real materialized siblings under a composite.

// edg_twoSiblings decomposes a fresh composite into exactly two leaf children
// (keys "up"/"down") with an optional hard edge down<-up, and returns the
// materialized (upstream, downstream) rows. review_policy=none, so
// SubmitDecomposition materializes the children, releases the leaves to 'ready',
// and leaves the composite 'active'. withEdge=false materializes the two siblings
// WITHOUT an edge so the caller can add one through the service path.
func edg_twoSiblings(h *harness, withEdge bool) (up, down sqlc.AgentGoal, composite sqlc.AgentGoal) {
	h.t.Helper()
	ctx := context.Background()
	composite = h.createRoot(KindComposite, AcceptanceContract{})

	content := DecompositionContent{
		Children: []ProposedChild{
			{Key: "up", Title: "upstream", Intent: "produce", Kind: KindLeaf, Required: true},
			{Key: "down", Title: "downstream", Intent: "consume", Kind: KindLeaf, Required: true},
		},
	}
	if withEdge {
		content.Edges = []ProposedEdge{
			{DownstreamKey: "down", UpstreamKey: "up", Kind: EdgeHard, OnFailure: OnFailureBlock},
		}
	}

	cmp_decompose(h.t, h, composite.ID, content)

	children, err := h.q.ListGoalChildren(ctx, pgnull.Text(composite.ID))
	if err != nil {
		h.t.Fatalf("edg: ListChildren: %v", err)
	}
	if len(children) != 2 {
		h.t.Fatalf("edg: want 2 children, got %d", len(children))
	}
	// Children are positioned by proposal index: 0=up, 1=down.
	for _, c := range children {
		switch c.Position {
		case 0:
			up = c
		case 1:
			down = c
		}
	}
	composite = h.get(composite.ID)
	return up, down, composite
}

// edg_edgeRow constructs one upstream-state-joined edge row for a Compute() unit
// test. upstreamLifecycle is the pre-joined upstream lifecycle; waived stamps a
// non-empty waived_at.
func edg_edgeRow(upstreamID, kind, onFailure, upstreamLifecycle, doneReason string, waived bool) sqlc.ListEdgeWithUpstreamStateRow {
	r := sqlc.ListEdgeWithUpstreamStateRow{
		UpstreamID:         upstreamID,
		EdgeKind:           kind,
		OnFailure:          onFailure,
		UpstreamLifecycle:  upstreamLifecycle,
		UpstreamDoneReason: doneReason,
	}
	if waived {
		r.WaivedAt = pgtype.Timestamptz{Time: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), Valid: true}
	}
	return r
}

// edg_readyLeaf is a ready leaf row to feed Compute (the row only needs the
// fields Compute reads: Lifecycle, Kind).
func edg_readyLeaf() sqlc.AgentGoal {
	return sqlc.AgentGoal{Lifecycle: LifecyclePending, Kind: KindLeaf}
}

func edg_hasReason(rs []Reason, typ string) bool {
	for _, r := range rs {
		if r.Type == typ {
			return true
		}
	}
	return false
}

// TestEdgReadinessHardUpstreamGate asserts the core readiness contract
// (readiness.go §): a HARD edge to a not-yet-accepted upstream parks the
// downstream in waiting_deps (NOT dispatchable) with an upstream_not_accepted
// reason; once the upstream is accepted (or the edge waived) the downstream
// becomes dispatchable.
func testReadiness(ctx context.Context, h *harness, id string) (Readiness, error) {
	d, err := h.q.GetGoal(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	edges, err := h.q.ListEdgeWithUpstreamState(ctx, id)
	if err != nil {
		return Readiness{}, err
	}
	return Compute(d, edges, h.svc.nowTime()), nil
}

func TestEdgReadinessHardUpstreamGate(t *testing.T) {
	now := time.Now().UTC()

	// Upstream still in progress (active) ⇒ hard wait, not dispatchable.
	r := Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeHard, OnFailureBlock, LifecycleActive, "", false),
	}, now)
	if r.Dispatchable || r.State != ReadinessWaitingDeps {
		t.Fatalf("unaccepted hard upstream: state=%q dispatchable=%v want waiting_deps/false", r.State, r.Dispatchable)
	}
	if !edg_hasReason(r.Reasons, "upstream_not_accepted") {
		t.Fatalf("want upstream_not_accepted reason, got %+v", r.Reasons)
	}

	// Upstream accepted ⇒ satisfied ⇒ dispatchable.
	r = Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeHard, OnFailureBlock, LifecycleDone, DoneReasonAccepted, false),
	}, now)
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("accepted hard upstream: state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}

	// Waived (but unaccepted) ⇒ satisfied ⇒ dispatchable.
	r = Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeHard, OnFailureBlock, LifecycleActive, "", true),
	}, now)
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("waived hard upstream: state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}
}

// TestEdgReadinessOnFailureSemantics asserts on_failure handling for a hard edge
// whose upstream reached a terminal-bad lifecycle (readiness.go §): block and
// (default/unknown) fail-safe to blocked(dep); fail propagates; ignore satisfies.
func TestEdgReadinessOnFailureSemantics(t *testing.T) {
	now := time.Now().UTC()

	// on_failure=block + failed upstream ⇒ blocked(dep) with upstream_failed_block.
	r := Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeHard, OnFailureBlock, LifecycleDone, DoneReasonFailed, false),
	}, now)
	if r.Dispatchable || r.State != ReadinessBlocked {
		t.Fatalf("block+failed: state=%q dispatchable=%v want blocked/false", r.State, r.Dispatchable)
	}
	if !edg_hasReason(r.Reasons, "upstream_failed_block") {
		t.Fatalf("want upstream_failed_block reason, got %+v", r.Reasons)
	}

	// on_failure=fail + failed upstream ⇒ blocked with upstream_failed_propagate.
	r = Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeHard, OnFailureFail, LifecycleDone, DoneReasonFailed, false),
	}, now)
	if r.Dispatchable || r.State != ReadinessBlocked {
		t.Fatalf("fail+failed: state=%q dispatchable=%v want blocked/false", r.State, r.Dispatchable)
	}
	if !edg_hasReason(r.Reasons, "upstream_failed_propagate") {
		t.Fatalf("want upstream_failed_propagate reason, got %+v", r.Reasons)
	}

	// on_failure=ignore + failed upstream ⇒ satisfied ⇒ dispatchable.
	r = Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeHard, OnFailureIgnore, LifecycleDone, DoneReasonFailed, false),
	}, now)
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("ignore+failed: state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}
}

// TestEdgReadinessSoftEdgeNeverGates asserts a SOFT edge is advisory only
// (readiness.go §): a pending soft upstream surfaces a soft_upstream_pending
// diagnostic but never flips dispatchable to false.
func TestEdgReadinessSoftEdgeNeverGates(t *testing.T) {
	now := time.Now().UTC()

	r := Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeSoft, OnFailureBlock, LifecycleActive, "", false),
	}, now)
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("soft pending upstream: state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}
	if !edg_hasReason(r.Reasons, "soft_upstream_pending") {
		t.Fatalf("want soft_upstream_pending diagnostic, got %+v", r.Reasons)
	}

	// A soft edge whose upstream FAILED also never blocks dispatch.
	r = Compute(edg_readyLeaf(), []sqlc.ListEdgeWithUpstreamStateRow{
		edg_edgeRow("up", EdgeSoft, OnFailureBlock, LifecycleDone, DoneReasonFailed, false),
	}, now)
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("soft failed upstream: state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}
}

// TestEdgReadinessNonReadyLifecycles asserts Compute short-circuits on
// non-ready lifecycles before edge evaluation (readiness.go §): a composite is
// gated by rollup (ReadinessComposite), a draft/active/terminal returns its
// lifecycle-derived state, and none of these are dispatchable.
func TestEdgReadinessNonReadyLifecycles(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		lifecycle string
		kind      string
		want      string
	}{
		{LifecycleDraft, KindLeaf, ReadinessDraft},
		{LifecycleActive, KindLeaf, ReadinessActive},
		{LifecycleDone, KindLeaf, ReadinessTerminal},
		{LifecycleDone, KindLeaf, ReadinessTerminal},
		{LifecyclePending, KindComposite, ReadinessComposite}, // ready composite ⇒ rollup-gated
	}
	for _, c := range cases {
		r := Compute(sqlc.AgentGoal{Lifecycle: c.lifecycle, Kind: c.kind}, nil, now)
		if r.State != c.want {
			t.Fatalf("lifecycle=%q kind=%q: state=%q want %q", c.lifecycle, c.kind, r.State, c.want)
		}
		if r.Dispatchable {
			t.Fatalf("lifecycle=%q kind=%q: dispatchable=true want false", c.lifecycle, c.kind)
		}
	}
}

// TestEdgAddEdgePersistsKindAndOnFailure asserts AddEdge between two real
// siblings inserts an edge row carrying the requested kind + on_failure (the
// joined readiness row is the audit surface). Defaults are also covered: empty
// kind/on_failure normalize to hard/block (service.go §).
func TestEdgAddEdgePersistsKindAndOnFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	up, down, _ := edg_twoSiblings(h, false)

	e, err := h.svc.AddEdge(ctx, down.ID, up.ID, EdgeSoft, OnFailureIgnore)
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if e.GoalID != down.ID || e.UpstreamID != up.ID {
		t.Fatalf("edge endpoints: down=%q up=%q want %q/%q", e.GoalID, e.UpstreamID, down.ID, up.ID)
	}
	if e.EdgeKind != EdgeSoft || e.OnFailure != OnFailureIgnore {
		t.Fatalf("edge row kind=%q on_failure=%q want soft/ignore", e.EdgeKind, e.OnFailure)
	}

	// Re-read via the joined readiness query to confirm durability.
	rows, err := h.q.ListEdgeWithUpstreamState(ctx, down.ID)
	if err != nil {
		t.Fatalf("ListEdgeWithUpstreamState: %v", err)
	}
	if len(rows) != 1 || rows[0].EdgeKind != EdgeSoft || rows[0].OnFailure != OnFailureIgnore {
		t.Fatalf("joined edge rows=%+v want one soft/ignore", rows)
	}

	// Empty kind/on_failure default to hard/block. Use a second composite so the
	// pair has no pre-existing edge.
	h2 := newHarness(t)
	up2, down2, _ := edg_twoSiblings(h2, false)
	e2, err := h2.svc.AddEdge(ctx, down2.ID, up2.ID, "", "")
	if err != nil {
		t.Fatalf("AddEdge defaults: %v", err)
	}
	if e2.EdgeKind != EdgeHard || e2.OnFailure != OnFailureBlock {
		t.Fatalf("default edge kind=%q on_failure=%q want hard/block", e2.EdgeKind, e2.OnFailure)
	}
}

// TestEdgAddEdgeCycleRejected asserts the DFS cycle guard (service.go §,
// types.go ErrCycle): a self-edge and a back-edge that would close a cycle both
// return ErrCycle and persist no row.
func TestEdgAddEdgeCycleRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	up, down, _ := edg_twoSiblings(h, false)

	// Self-edge is a trivial cycle.
	if _, err := h.svc.AddEdge(ctx, down.ID, down.ID, EdgeHard, OnFailureBlock); !errors.Is(err, ErrCycle) {
		t.Fatalf("self-edge err=%v want ErrCycle", err)
	}

	// down depends on up (down←up). Now up←down would close a cycle.
	if _, err := h.svc.AddEdge(ctx, down.ID, up.ID, EdgeHard, OnFailureBlock); err != nil {
		t.Fatalf("seed edge down←up: %v", err)
	}
	if _, err := h.svc.AddEdge(ctx, up.ID, down.ID, EdgeHard, OnFailureBlock); !errors.Is(err, ErrCycle) {
		t.Fatalf("back-edge up←down err=%v want ErrCycle", err)
	}

	// The rejected back-edge left no row: up has no upstreams.
	upEdges, err := h.q.ListEdgeByGoal(ctx, up.ID)
	if err != nil {
		t.Fatalf("ListEdgeByGoal: %v", err)
	}
	if len(upEdges) != 0 {
		t.Fatalf("rejected cycle persisted %d edges on up", len(upEdges))
	}
}

// TestEdgReadinessEndToEndAcceptUnblocks wires two real siblings with a hard
// edge and asserts the downstream is gated until the upstream accepts — then
// dispatchable. This exercises the joined GetReadiness path (boot.go) against
// live upstream lifecycle, not constructed rows.
func TestEdgReadinessEndToEndAcceptUnblocks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	up, down, _ := edg_twoSiblings(h, true)

	// SubmitDecomposition (review_policy=none) already released both leaves to ready.
	if got := h.get(up.ID).Lifecycle; got != LifecyclePending {
		t.Fatalf("upstream after decomposition=%q want ready", got)
	}
	if got := h.get(down.ID).Lifecycle; got != LifecyclePending {
		t.Fatalf("downstream after decomposition=%q want ready", got)
	}

	// Downstream is ready-lifecycle but NOT dispatchable: hard upstream unaccepted.
	r, err := testReadiness(ctx, h, down.ID)
	if err != nil {
		t.Fatalf("GetReadiness(pre): %v", err)
	}
	if r.Dispatchable || r.State != ReadinessWaitingDeps {
		t.Fatalf("pre-accept downstream readiness state=%q dispatchable=%v want waiting_deps/false", r.State, r.Dispatchable)
	}

	// Run the upstream leaf to accepted (trivial contract auto-accepts).
	h.runLeaf(up.ID)
	if got := h.get(up.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("upstream after run=%q want accepted", got)
	}

	// Now the hard edge is satisfied ⇒ downstream is dispatchable.
	r, err = testReadiness(ctx, h, down.ID)
	if err != nil {
		t.Fatalf("GetReadiness(post): %v", err)
	}
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("post-accept downstream readiness state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}
}

// TestEdgWaiveEdgeStampsEdge asserts WaiveEdge stamps the edge waived; readiness
// then treats the hard edge as satisfied without writing dependency block state.
func TestEdgWaiveEdgeStampsEdge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	up, down, _ := edg_twoSiblings(h, true)

	// Waive the hard edge: the edge is stamped waived; downstream lifecycle is not
	// mutated because dependency blocking is derived, not stored.
	if err := h.svc.WaiveEdge(ctx, down.ID, up.ID, "manual override", UserActor(h.userID)); err != nil {
		t.Fatalf("WaiveEdge: %v", err)
	}

	edge, err := h.q.GetEdge(ctx, sqlc.GetEdgeParams{GoalID: down.ID, UpstreamID: up.ID})
	if err != nil {
		t.Fatalf("GetEdge after waive: %v", err)
	}
	if !edge.WaivedAt.Valid {
		t.Fatalf("edge waived_at not stamped: %+v", edge.WaivedAt)
	}
	if edge.WaiverReason != "manual override" {
		t.Fatalf("edge waiver_reason=%q want %q", edge.WaiverReason, "manual override")
	}

	if got := h.get(down.ID).Lifecycle; got != LifecyclePending {
		t.Fatalf("downstream after waive lifecycle=%q want unchanged pending", got)
	}

	// Readiness now reports it dispatchable (the waived hard edge is satisfied
	// even though the upstream never accepted).
	r, err := testReadiness(ctx, h, down.ID)
	if err != nil {
		t.Fatalf("GetReadiness after waive: %v", err)
	}
	if !r.Dispatchable || r.State != ReadinessDispatchable {
		t.Fatalf("waived downstream readiness state=%q dispatchable=%v want dispatchable/true", r.State, r.Dispatchable)
	}
}

// TestEdgWaiveEdgeMissingEdge asserts WaiveEdge on a non-existent edge returns
// ErrNotFound (service.go §).
func TestEdgWaiveEdgeMissingEdge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	up, down, _ := edg_twoSiblings(h, false) // no edge materialized

	if err := h.svc.WaiveEdge(ctx, down.ID, up.ID, "x", UserActor(h.userID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("WaiveEdge(missing) err=%v want ErrNotFound", err)
	}
}
