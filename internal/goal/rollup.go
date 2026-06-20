package goal

import "github.com/CherryHQ/stella/pkg/db/sqlc"

// RollupVerdict is the decision RollupComposite returns over a composite
// parent's incremental rollup counters. The service acts on exactly one.
type RollupVerdict string

const (
	// RollupWait — required children still in flight; no transition (no-op).
	RollupWait RollupVerdict = "wait"
	// RollupAcceptParent — all required children accepted; run the parent's own
	// Accept gate (its contract; trivial ⇒ immediate accept).
	RollupAcceptParent RollupVerdict = "accept_parent"
	// RollupBlock — a required child is blocked; the parent surfaces the stall.
	RollupBlock RollupVerdict = "block"
	// RollupFail — a required child reached a terminal-bad state
	// (rejected_final/abandoned/cancelled); the requirement is permanently unmet.
	RollupFail RollupVerdict = "fail"
)

// RollupComposite is the PURE decision over a composite parent's incremental
// required_* counters (contract §6). It never scans the subtree — the counters
// are bumped in the same tx that transitions each child, so the parent reads
// O(1) state. Precedence mirrors the goal-rollup table: a failed required child
// fails the parent; else a blocked one blocks it; else all-accepted accepts it;
// else wait.
//
// A parent with no required children yet (required_total == 0) waits: the
// materializer may still be inserting children, and a vacuous "all required
// accepted" must never auto-accept a composite that has nothing to roll up.
func RollupComposite(parent sqlc.AgentGoal) RollupVerdict {
	// Only an active composite rolls up. Terminal/draft/ready/blocked parents are
	// not driven by this decision.
	if parent.Kind != KindComposite || parent.Lifecycle != LifecycleActive {
		return RollupWait
	}
	if parent.RequiredTotal <= 0 {
		return RollupWait
	}
	switch {
	case parent.RequiredFailed > 0:
		return RollupFail
	case parent.RequiredBlocked > 0:
		return RollupBlock
	case parent.RequiredAccepted >= parent.RequiredTotal:
		return RollupAcceptParent
	default:
		return RollupWait
	}
}
