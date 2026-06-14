package tasks

import (
	"database/sql"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestRollupGoal(t *testing.T) {
	count := func(done, failed, blocked, pending int) sqlc.GoalChildCountsRow {
		f := func(v int) sql.NullFloat64 { return sql.NullFloat64{Float64: float64(v), Valid: true} }
		return sqlc.GoalChildCountsRow{
			RequiredDone: f(done), RequiredFailed: f(failed),
			RequiredBlocked: f(blocked), RequiredPending: f(pending),
		}
	}

	tests := []struct {
		name         string
		goalStatus   string
		policy       string
		counts       sqlc.GoalChildCountsRow
		hasOpenSynth bool
		wantNext     string
		wantSpawn    bool
	}{
		{"draft-no-op", GoalStatusDraft, ReviewPolicyNone, count(0, 0, 0, 2), false, "", false},
		{"reviewing-no-op", GoalStatusReviewing, ReviewPolicyHuman, count(2, 0, 0, 0), false, "", false},
		// An empty goal (planner hasn't inserted tasks yet) or one with only
		// optional children must not vacuously auto-complete.
		{"running-no-required-children", GoalStatusRunning, ReviewPolicyNone, count(0, 0, 0, 0), false, "", false},
		{"blocked-no-required-children", GoalStatusBlocked, ReviewPolicyNone, count(0, 0, 0, 0), false, "", false},
		{"running-pending", GoalStatusRunning, ReviewPolicyNone, count(1, 0, 0, 2), false, GoalStatusRunning, false},
		{"running-failed-child", GoalStatusRunning, ReviewPolicyNone, count(1, 1, 0, 0), false, GoalStatusFailed, false},
		{"running-blocked-child", GoalStatusRunning, ReviewPolicyNone, count(1, 0, 1, 0), false, GoalStatusBlocked, false},
		{"running-all-done-none", GoalStatusRunning, ReviewPolicyNone, count(3, 0, 0, 0), false, GoalStatusDone, false},
		{"running-all-done-auto-spawn", GoalStatusRunning, ReviewPolicyAuto, count(3, 0, 0, 0), false, "", true},
		{"running-all-done-human-spawn", GoalStatusRunning, ReviewPolicyHuman, count(3, 0, 0, 0), false, "", true},
		{"running-all-done-synth-in-flight", GoalStatusRunning, ReviewPolicyHuman, count(3, 0, 0, 0), true, "", false},
		{"running-failed-beats-blocked", GoalStatusRunning, ReviewPolicyNone, count(1, 1, 1, 0), false, GoalStatusFailed, false},
		// Blocked goals keep rolling so they recover: a cleared blocker
		// (pending>0, blocked=0) returns the goal to running; an unchanged
		// blocker re-asserts blocked (caller skips that no-op); all-done
		// completes; a child failure escalates to failed.
		{"blocked-recovers-to-running", GoalStatusBlocked, ReviewPolicyNone, count(1, 0, 0, 2), false, GoalStatusRunning, false},
		{"blocked-stays-blocked", GoalStatusBlocked, ReviewPolicyNone, count(1, 0, 1, 0), false, GoalStatusBlocked, false},
		{"blocked-all-done-completes", GoalStatusBlocked, ReviewPolicyNone, count(3, 0, 0, 0), false, GoalStatusDone, false},
		{"blocked-child-fails", GoalStatusBlocked, ReviewPolicyNone, count(1, 1, 0, 0), false, GoalStatusFailed, false},
		{"terminal-no-op", GoalStatusDone, ReviewPolicyNone, count(3, 0, 0, 0), false, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			goal := sqlc.AgentGoal{Status: tc.goalStatus, ReviewPolicy: tc.policy}
			got := RollupGoal(goal, tc.counts, tc.hasOpenSynth)
			if got.NextStatus != tc.wantNext {
				t.Errorf("NextStatus=%q want %q", got.NextStatus, tc.wantNext)
			}
			if got.SpawnSynthesizer != tc.wantSpawn {
				t.Errorf("SpawnSynthesizer=%v want %v", got.SpawnSynthesizer, tc.wantSpawn)
			}
		})
	}
}

// A cancelled required child can never be reopened, so it must not let a goal
// vacuously complete: the goal fails instead of falling through to "all done".
func TestRollupGoal_CancelledRequiredChild(t *testing.T) {
	f := func(v int) sql.NullFloat64 { return sql.NullFloat64{Float64: float64(v), Valid: true} }
	row := func(done, failed, cancelled, blocked, pending int) sqlc.GoalChildCountsRow {
		return sqlc.GoalChildCountsRow{
			RequiredDone: f(done), RequiredFailed: f(failed), RequiredCancelled: f(cancelled),
			RequiredBlocked: f(blocked), RequiredPending: f(pending),
		}
	}
	tests := []struct {
		name       string
		goalStatus string
		counts     sqlc.GoalChildCountsRow
		wantNext   string
		wantReason string
	}{
		{"running-done-plus-cancelled", GoalStatusRunning, row(1, 0, 1, 0, 0), GoalStatusFailed, "required_child_cancelled"},
		{"running-only-cancelled", GoalStatusRunning, row(0, 0, 1, 0, 0), GoalStatusFailed, "required_child_cancelled"},
		{"failed-done-plus-cancelled-stays-failed", GoalStatusFailed, row(1, 0, 1, 0, 0), GoalStatusFailed, "required_child_cancelled"},
		{"failed-beats-cancelled", GoalStatusRunning, row(0, 1, 1, 0, 0), GoalStatusFailed, "required_child_failed"},
		{"cancelled-beats-blocked", GoalStatusRunning, row(0, 0, 1, 1, 0), GoalStatusFailed, "required_child_cancelled"},
		{"cancelled-beats-pending", GoalStatusRunning, row(0, 0, 1, 0, 1), GoalStatusFailed, "required_child_cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			goal := sqlc.AgentGoal{Status: tc.goalStatus, ReviewPolicy: ReviewPolicyNone}
			got := RollupGoal(goal, tc.counts, false)
			if got.NextStatus != tc.wantNext {
				t.Errorf("NextStatus=%q want %q", got.NextStatus, tc.wantNext)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason=%q want %q", got.Reason, tc.wantReason)
			}
		})
	}
}
