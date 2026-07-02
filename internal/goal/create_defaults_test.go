package goal

import "testing"

// TestCreateRoot_NormalizesConvergencePolicy pins that a goal created without an
// explicit convergence policy persists the effective defaults, not a bare zero
// policy. The create response then shows real values (max_attempts 3, block,
// depth 4, planner repair 2, concurrent 8) instead of a confusing
// max_attempts:0, and the goal's budget is frozen at create rather than depending
// on every runtime caller to Normalize.
func TestCreateRoot_NormalizesConvergencePolicy(t *testing.T) {
	h := newHarness(t)
	root := h.createRoot(KindLeaf, AcceptanceContract{})

	var pol ConvergencePolicy
	if err := unmarshalJSON(root.ConvergencePolicy, &pol); err != nil {
		t.Fatalf("decode convergence_policy: %v", err)
	}
	want := ConvergencePolicy{
		MaxAttempts:      defaultMaxAttempts,
		Escalation:       EscalationBlock,
		MaxDepth:         defaultMaxDepth,
		PlannerRepairMax: defaultPlannerRepairMax,
		MaxConcurrent:    defaultMaxConcurrent,
	}
	if pol != want {
		t.Fatalf("convergence_policy = %+v, want defaults %+v applied at create", pol, want)
	}
}
