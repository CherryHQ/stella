package tasks

import (
	"database/sql"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Goal status constants. Mirrors the agent_goal.status CHECK.
const (
	GoalStatusDraft     = "draft"
	GoalStatusPlanning  = "planning"
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
	// Terminal / quiescent states are not rolled.
	switch goal.Status {
	case GoalStatusDone, GoalStatusFailed, GoalStatusCancelled,
		GoalStatusReviewing, GoalStatusDraft, GoalStatusPlanning, GoalStatusBlocked:
		return GoalNextState{}
	}
	// goal.Status == running from here on.

	failed := nullFloatToInt(counts.RequiredFailed)
	blocked := nullFloatToInt(counts.RequiredBlocked)
	pending := nullFloatToInt(counts.RequiredPending)

	if failed > 0 {
		return GoalNextState{NextStatus: GoalStatusFailed, Reason: "required_child_failed"}
	}
	if blocked > 0 {
		return GoalNextState{NextStatus: GoalStatusBlocked, Reason: "required_child_blocked"}
	}
	if pending > 0 {
		return GoalNextState{}
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
