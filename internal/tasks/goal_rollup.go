package tasks

import (
	"database/sql"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Goal status constants. Mirrors the agent_goal.status CHECK.
const (
	GoalStatusDraft     = "draft"
	GoalStatusPlanning  = "planning"
	GoalStatusPlanned   = "planned"
	GoalStatusRunning   = "running"
	GoalStatusBlocked   = "blocked"
	GoalStatusReviewing = "reviewing"
	GoalStatusDone      = "done"
	GoalStatusFailed    = "failed"
	GoalStatusCancelled = "cancelled"
)

// GoalNextState is the outcome of one rollup evaluation.
type GoalNextState struct {
	// NextStatus is the status the goal should transition to. Empty when no
	// transition applies (rollup is a no-op).
	NextStatus string

	// SpawnSynthesizer is set when a 'running' goal has met its required-done
	// threshold and the policy demands a synthesis pass before completion.
	SpawnSynthesizer bool

	// Reason carries a short human-readable cause; surfaced on the event row.
	Reason string
}

// RollupGoal applies the goal-rollup decision table (plan D2) to a single
// goal. Pure function over (goal row, child counts, whether a synthesizer
// run is already in flight). Caller (dispatcher.rollupGoals) translates the
// outcome into a transition call.
func RollupGoal(goal sqlc.AgentGoal, counts sqlc.GoalChildCountsRow, hasOpenSynth bool) GoalNextState {
	// Terminal / quiescent states are not rolled. 'blocked' and 'failed' are
	// intentionally NOT quiescent: a blocked goal must recover once children
	// unblock, and a failed goal must recover when a failed required child is
	// later reopened, retried, or completed.
	switch goal.Status {
	case GoalStatusDone, GoalStatusCancelled,
		GoalStatusReviewing, GoalStatusDraft, GoalStatusPlanning, GoalStatusPlanned:
		return GoalNextState{}
	}
	// goal.Status is running, blocked, or failed from here on.

	done := nullFloatToInt(counts.RequiredDone)
	failed := nullFloatToInt(counts.RequiredFailed)
	cancelled := nullFloatToInt(counts.RequiredCancelled)
	blocked := nullFloatToInt(counts.RequiredBlocked)
	pending := nullFloatToInt(counts.RequiredPending)

	// No required children yet: the planner may still be inserting tasks when
	// the dispatcher tick fires, and a goal with only optional children has no
	// completion signal. Never auto-complete on a vacuous "all required done";
	// explicit CompleteGoal remains available.
	if done+failed+cancelled+blocked+pending == 0 {
		return GoalNextState{}
	}

	if failed > 0 {
		return GoalNextState{NextStatus: GoalStatusFailed, Reason: "required_child_failed"}
	}
	if cancelled > 0 {
		// A cancelled required child can never be reopened (D10), so the
		// requirement is permanently unmet. Fail the goal rather than letting it
		// fall through to "all required done" and vacuously complete when the
		// only non-done required children were abandoned. Explicit CompleteGoal
		// stays available to override.
		return GoalNextState{NextStatus: GoalStatusFailed, Reason: "required_child_cancelled"}
	}
	if blocked > 0 {
		return GoalNextState{NextStatus: GoalStatusBlocked, Reason: "required_child_blocked"}
	}
	if pending > 0 {
		// Required children still in flight and none blocked. A running goal
		// stays running (no-op); a blocked goal recovers to running.
		return GoalNextState{NextStatus: GoalStatusRunning, Reason: "required_children_pending"}
	}

	// All required children done.
	if goal.ReviewPolicy == ReviewPolicyNone {
		return GoalNextState{NextStatus: GoalStatusDone, Reason: "required_children_done"}
	}
	if hasOpenSynth {
		return GoalNextState{}
	}
	return GoalNextState{SpawnSynthesizer: true, Reason: "required_children_done"}
}

// nullFloatToInt converts a NullFloat64 aggregate (SQLite SUM() returns
// REAL via sqlc) to a plain int64. NULL => 0.
func nullFloatToInt(v sql.NullFloat64) int64 {
	if !v.Valid {
		return 0
	}
	return int64(v.Float64)
}
