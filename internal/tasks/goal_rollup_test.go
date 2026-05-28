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
		{"running-pending", GoalStatusRunning, ReviewPolicyNone, count(1, 0, 0, 2), false, "", false},
		{"running-failed-child", GoalStatusRunning, ReviewPolicyNone, count(1, 1, 0, 0), false, GoalStatusFailed, false},
		{"running-blocked-child", GoalStatusRunning, ReviewPolicyNone, count(1, 0, 1, 0), false, GoalStatusBlocked, false},
		{"running-all-done-none", GoalStatusRunning, ReviewPolicyNone, count(3, 0, 0, 0), false, GoalStatusDone, false},
		{"running-all-done-auto-spawn", GoalStatusRunning, ReviewPolicyAuto, count(3, 0, 0, 0), false, "", true},
		{"running-all-done-human-spawn", GoalStatusRunning, ReviewPolicyHuman, count(3, 0, 0, 0), false, "", true},
		{"running-all-done-synth-in-flight", GoalStatusRunning, ReviewPolicyHuman, count(3, 0, 0, 0), true, "", false},
		{"running-failed-beats-blocked", GoalStatusRunning, ReviewPolicyNone, count(1, 1, 1, 0), false, GoalStatusFailed, false},
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
