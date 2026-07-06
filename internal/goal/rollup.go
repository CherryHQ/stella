package goal

import "github.com/CherryHQ/stella/pkg/db/sqlc"

// RollupVerdict is the decision RollupComposite returns over a composite
// parent's required child tally. The service acts on exactly one.
type RollupVerdict string

const (
	// RollupWait — required children still in flight; no transition (no-op).
	RollupWait RollupVerdict = "wait"
	// RollupAcceptParent — all required children accepted; run the parent's own
	// Accept gate (its contract; trivial => immediate accept).
	RollupAcceptParent RollupVerdict = "accept_parent"
	// RollupBlock — a required child is blocked by a human/environment issue.
	RollupBlock RollupVerdict = "block"
	// RollupFail — a required child is done(failed/cancelled), including a derived
	// dependency death with on_failure=fail.
	RollupFail RollupVerdict = "fail"
)

// RollupComposite is the PURE decision over a composite parent's derived child
// tally. Stored rollup counters were deliberately removed; the query derives
// dependency death from edges and upstream done_reason at read time.
func RollupComposite(parent sqlc.AgentGoal, tally sqlc.GetRequiredChildRollupCountsRow) RollupVerdict {
	if parent.Kind != KindComposite || parent.Lifecycle != LifecycleActive {
		return RollupWait
	}
	if tally.Total <= 0 {
		return RollupWait
	}
	switch {
	case tally.Failed+tally.DepFailed > 0:
		return RollupFail
	case tally.Blocked > 0:
		return RollupBlock
	case tally.Accepted >= tally.Total:
		return RollupAcceptParent
	default:
		return RollupWait
	}
}
