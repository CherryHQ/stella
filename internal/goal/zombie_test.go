package goal

import (
	"context"
	"testing"
	"time"
)

// forceZombieState writes a raw lifecycle/updated_at, bypassing the service
// (the transition table forbids these states — that is the point: the scan is
// the backstop for rows that got there out-of-band or pre-date the table).
func (h *harness) forceZombieState(id, lifecycle string, age time.Duration) {
	h.t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		UPDATE agent_goal
		SET lifecycle = $2, active_attempt_id = NULL, updated_at = now() - $3::interval
		WHERE id = $1`, id, lifecycle, age.String()); err != nil {
		h.t.Fatalf("force zombie state: %v", err)
	}
}

// TestListZombieGoals pins the liveness backstop's predicate: it must flag a
// ready composite and a stranded active goal (no attempt pointer, nothing in
// flight, not rollup-driven), and must NOT flag rollup-driven planned
// composites, recently-touched rows (grace), or terminal rows.
func TestListZombieGoals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Zombie 1: a composite in ready — nothing claims it.
	readyComposite := h.createRoot(KindComposite, AcceptanceContract{})
	h.forceZombieState(readyComposite.ID, LifecyclePending, 10*time.Minute)

	// Zombie 2: a stranded active leaf — no pointer, no in-flight attempt.
	strandedLeaf := h.createRoot(KindLeaf, AcceptanceContract{})
	h.forceZombieState(strandedLeaf.ID, LifecycleActive, 10*time.Minute)

	// Control 1: a PLANNED active composite is rollup-driven, not a zombie.
	planned := h.createRoot(KindComposite, AcceptanceContract{})
	cmp_decompose(t, h, planned.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	})
	h.forceZombieState(planned.ID, LifecycleActive, 10*time.Minute)

	// Control 2: a fresh ready composite is inside the grace window.
	freshReady := h.createRoot(KindComposite, AcceptanceContract{})
	h.forceZombieState(freshReady.ID, LifecyclePending, time.Minute)

	// Control 3: terminal rows are never zombies.
	cancelled := h.createRoot(KindLeaf, AcceptanceContract{})
	if err := h.svc.Cancel(ctx, cancelled.ID, "done with it", UserActor(h.userID)); err != nil {
		t.Fatalf("cancel control: %v", err)
	}

	rows, err := h.q.ListZombieGoals(ctx, 50)
	if err != nil {
		t.Fatalf("ListZombieGoals: %v", err)
	}
	got := map[string]bool{}
	for _, g := range rows {
		got[g.ID] = true
	}
	if !got[readyComposite.ID] {
		t.Errorf("ready composite %s not flagged", readyComposite.ID)
	}
	if !got[strandedLeaf.ID] {
		t.Errorf("stranded active leaf %s not flagged", strandedLeaf.ID)
	}
	for name, id := range map[string]string{
		"planned active composite": planned.ID,
		"fresh ready composite":    freshReady.ID,
		"cancelled leaf":           cancelled.ID,
	} {
		if got[id] {
			t.Errorf("%s %s wrongly flagged as zombie", name, id)
		}
	}
}
