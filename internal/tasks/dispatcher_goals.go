package tasks

import (
	"context"
	"database/sql"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// rollupGoals iterates active or recoverable goals and applies RollupGoal's verdict.
// Driven from the dispatcher tick.
func (d *Dispatcher) rollupGoals(ctx context.Context, _ time.Time) {
	goals, err := d.cfg.Queries.ListAgentGoals(ctx, sqlc.ListAgentGoalsParams{
		Limit: int64(d.cfg.BatchLimit), Offset: 0,
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list goals", "err", err)
		return
	}
	for _, g := range goals {
		if isQuiescentGoalStatus(g.Status) {
			continue
		}
		d.rollupOneGoal(ctx, g)
	}
}

func (d *Dispatcher) rollupOneGoal(ctx context.Context, g sqlc.AgentGoal) {
	counts, err := d.cfg.Queries.GoalChildCounts(ctx, sql.NullString{String: g.ID, Valid: true})
	if err != nil {
		return
	}
	hasOpenSynth := d.hasOpenRunForGoal(ctx, g.ID, RunKindSynthesizer)
	next := RollupGoal(g, counts, hasOpenSynth)
	if next.NextStatus == "" || next.NextStatus == g.Status {
		return // steady state or dormant SpawnSynthesizer; nothing to apply
	}
	switch next.NextStatus {
	case GoalStatusDone:
		_ = d.cfg.Service.CompleteGoal(ctx, g.ID, g.Output, SystemActor())
	case GoalStatusFailed:
		_ = d.cfg.Service.FailGoal(ctx, g.ID, next.Reason, SystemActor())
	case GoalStatusBlocked:
		_ = d.cfg.Service.BlockGoal(ctx, g.ID, next.Reason, SystemActor())
	case GoalStatusRunning:
		// Recovery: a blocked or failed goal whose children no longer justify the
		// blocked/failed state returns to running.
		_ = d.cfg.Service.UnblockGoal(ctx, g.ID, next.Reason, SystemActor())
	}
	// D8: SpawnSynthesizer stays dormant in this slice. RollupGoal still
	// computes the verdict, but no dispatch path consumes it until the
	// synthesizer runtime is wired. hasOpenSynth is read so the rollup
	// treats an in-flight synthesizer as "not done yet".
}

// hasOpenRunForGoal reports whether a queued/running run of the given kind
// exists for the goal. Used to short-circuit dispatch when one is already
// in flight (the unique indexes provide DB-level enforcement too).
func (d *Dispatcher) hasOpenRunForGoal(ctx context.Context, goalID, kind string) bool {
	run, err := d.cfg.Queries.LatestAgentTaskRunForGoal(ctx, sqlc.LatestAgentTaskRunForGoalParams{
		GoalID: nullable(goalID), Kind: kind,
	})
	if err != nil {
		return false
	}
	return IsActiveRunStatus(run.Status)
}
