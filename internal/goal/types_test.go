package goal

import "testing"

// allLifecycles enumerates every lifecycle for exhaustive table sweeps.
var allLifecycles = []string{
	LifecycleDraft, LifecyclePending, LifecycleActive, LifecycleBlocked,
	LifecycleDone, LifecycleDone, LifecycleDone, LifecycleDone,
}

// TestLegalLifecycleTransition_Invariants pins the state-machine table's
// structural invariants — the exact classes that shipped as zombies:
//   - a composite is never 'ready' (nothing claims it),
//   - a terminal goal is never resurrected,
//   - a leaf never re-enters 'draft',
//   - unknown kinds/states are illegal (fail loud, not open).
func TestLegalLifecycleTransition_Invariants(t *testing.T) {
	for _, from := range allLifecycles {
		if LegalLifecycleTransition(KindComposite, from, LifecyclePending) {
			t.Errorf("composite %s -> ready must be illegal (nothing claims a ready composite)", from)
		}
	}
	for _, term := range []string{LifecycleDone, LifecycleDone, LifecycleDone, LifecycleDone} {
		for _, to := range allLifecycles {
			if LegalLifecycleTransition(KindLeaf, term, to) || LegalLifecycleTransition(KindComposite, term, to) {
				t.Errorf("terminal %s -> %s must be illegal (resurrection)", term, to)
			}
		}
	}
	for _, from := range allLifecycles {
		if LegalLifecycleTransition(KindLeaf, from, LifecycleDraft) {
			t.Errorf("leaf %s -> draft must be illegal (leaves never re-enter draft)", from)
		}
	}
	if LegalLifecycleTransition("nonsense", LifecyclePending, LifecycleActive) ||
		LegalLifecycleTransition(KindLeaf, "nonsense", LifecycleActive) ||
		LegalLifecycleTransition(KindLeaf, LifecyclePending, "nonsense") {
		t.Errorf("unknown kind/state must be illegal")
	}
}

// TestLegalLifecycleTransition_KnownEdges spot-checks the edges every routing
// path depends on, so a table typo cannot silently disable a whole route.
func TestLegalLifecycleTransition_KnownEdges(t *testing.T) {
	legal := []struct{ kind, from, to string }{
		{KindLeaf, LifecycleDraft, LifecyclePending},       // releaseChildren / Activate
		{KindLeaf, LifecyclePending, LifecycleActive},      // claim
		{KindLeaf, LifecycleActive, LifecyclePending},      // rework / refund
		{KindLeaf, LifecycleActive, LifecycleBlocked},      // verdict / env / budget
		{KindLeaf, LifecycleBlocked, LifecyclePending},     // recovery / reattempt
		{KindLeaf, LifecycleBlocked, LifecycleActive},      // wake on verdict
		{KindLeaf, LifecycleBlocked, LifecycleDone},        // human give-up
		{KindComposite, LifecycleDraft, LifecycleActive},   // begin decomposition
		{KindComposite, LifecycleActive, LifecycleDraft},   // recover decomposition
		{KindComposite, LifecycleActive, LifecycleBlocked}, // plan gate / rollup block
		{KindComposite, LifecycleBlocked, LifecycleActive}, // approve plan / recover
		{KindComposite, LifecycleBlocked, LifecycleDraft},  // reject plan
		{KindComposite, LifecycleActive, LifecycleDone},    // rollup / fold accept
	}
	for _, e := range legal {
		if !LegalLifecycleTransition(e.kind, e.from, e.to) {
			t.Errorf("%s %s -> %s must be legal", e.kind, e.from, e.to)
		}
	}
}
