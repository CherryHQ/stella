package tasks

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// rollupGoals iterates non-terminal goals and applies RollupGoal's verdict.
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
		if isTerminalGoalStatus(g.Status) {
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
	switch next.NextStatus {
	case GoalStatusDone:
		_ = d.cfg.Service.CompleteGoal(ctx, g.ID, g.Output, SystemActor())
	case GoalStatusFailed:
		_ = d.cfg.Service.FailGoal(ctx, g.ID, next.Reason, SystemActor())
	case GoalStatusBlocked:
		_ = d.cfg.Service.BlockGoal(ctx, g.ID, next.Reason, SystemActor())
	}
	// SpawnSynthesizer is handled in scanAndDispatchSynthesizers, which also
	// re-evaluates the rollup against the latest state.
}

// scanAndDispatchPlanners spawns one planner run per draft goal that doesn't
// already have an in-flight planner. With the noop runner the run is created
// and immediately marked failed; the protocol_error event makes the
// "no real runner wired" state observable.
func (d *Dispatcher) scanAndDispatchPlanners(ctx context.Context, now time.Time) {
	candidates, err := d.cfg.Queries.ListGoalPlanningCandidates(ctx, int64(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list planning candidates", "err", err)
		return
	}
	for _, g := range candidates {
		if d.hasOpenRunForGoal(ctx, g.ID, RunKindPlanner) {
			continue
		}
		executor, ok := d.resolveGoalExecutor(g)
		if !ok {
			d.emitGoalProtocolError(ctx, g.ID, "no executor for planner")
			continue
		}
		d.dispatchGoalRun(ctx, g, RunKindPlanner, executor, now)
	}
}

// scanAndDispatchSynthesizers spawns one synthesizer run per goal whose
// rollup says it's time to synthesize.
func (d *Dispatcher) scanAndDispatchSynthesizers(ctx context.Context, now time.Time) {
	candidates, err := d.cfg.Queries.ListGoalSynthesisCandidates(ctx, int64(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list synth candidates", "err", err)
		return
	}
	for _, g := range candidates {
		counts, err := d.cfg.Queries.GoalChildCounts(ctx, sql.NullString{String: g.ID, Valid: true})
		if err != nil {
			continue
		}
		if d.hasOpenRunForGoal(ctx, g.ID, RunKindSynthesizer) {
			continue
		}
		next := RollupGoal(g, counts, false)
		if !next.SpawnSynthesizer {
			continue
		}
		executor, ok := d.resolveGoalExecutor(g)
		if !ok {
			d.emitGoalProtocolError(ctx, g.ID, "no executor for synthesizer")
			continue
		}
		d.dispatchGoalRun(ctx, g, RunKindSynthesizer, executor, now)
	}
}

// scanAndDispatchReviewers spawns one reviewer run per open agent review.
// Covers both task- and goal-parented reviews.
func (d *Dispatcher) scanAndDispatchReviewers(ctx context.Context, now time.Time) {
	reviews, err := d.cfg.Queries.ListOpenAgentReviewsForDispatch(ctx, int64(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list reviewer candidates", "err", err)
		return
	}
	for _, rev := range reviews {
		d.dispatchReviewerRun(ctx, rev, now)
	}
}

// dispatchGoalRun is the shared "create goal-parented run + run via runner"
// path used by planner and synthesizer scans. The noop runner immediately
// fails the run with a protocol_error.
func (d *Dispatcher) dispatchGoalRun(ctx context.Context, g sqlc.AgentGoal, kind, executorAgentID string, now time.Time) {
	sessionID, err := d.cfg.NewSession(ctx, sqlc.AgentTask{UserID: g.UserID, AgentID: nullable(executorAgentID)}, executorAgentID)
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: mint goal session", "goal", g.ID, "err", err)
		return
	}
	runID := uuid.NewString()
	nextAttempt, err := d.cfg.Queries.NextAttemptNoForGoal(ctx, sqlc.NextAttemptNoForGoalParams{
		GoalID: nullable(g.ID), Kind: kind,
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: goal next attempt", "goal", g.ID, "err", err)
		return
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	if _, err := d.cfg.Queries.CreateAgentTaskRun(ctx, sqlc.CreateAgentTaskRunParams{
		ID:              runID,
		TaskID:          sql.NullString{},
		GoalID:          nullable(g.ID),
		UserID:          g.UserID,
		AgentID:         nullable(executorAgentID),
		ExecutorAgentID: nullable(executorAgentID),
		Kind:            kind,
		AttemptNo:       nextAttempt,
		Status:          RunQueued,
		SessionID:       sessionID,
		Input:           "{}",
		LeaseExpiresAt:  sql.NullString{},
		WorkerID:        "",
		StartedAt:       sql.NullString{},
		CreatedAt:       nowStr,
		UpdatedAt:       nowStr,
	}); err != nil {
		d.cfg.Logger.Warn("dispatcher: create goal run", "goal", g.ID, "kind", kind, "err", err)
		return
	}
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		GoalID:    nullable(g.ID),
		RunID:     nullable(runID),
		EventType: "dispatch_" + kind,
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"executor_agent_id": executorAgentID, "session_id": sessionID}),
	})
	// Noop runner fallback: immediately fail the run. The runner adapter
	// replaces this with real execution.
	d.failGoalRunAsNoop(ctx, g.ID, runID, kind)
}

// dispatchReviewerRun spawns a reviewer run for an open agent review and
// repoints the review at it.
func (d *Dispatcher) dispatchReviewerRun(ctx context.Context, rev sqlc.AgentReview, now time.Time) {
	userID := ""
	executorAgentID := ""
	switch {
	case rev.TaskID.Valid:
		task, err := d.cfg.Queries.GetAgentTask(ctx, rev.TaskID.String)
		if err != nil {
			return
		}
		userID = task.UserID
		if task.AgentID.Valid {
			executorAgentID = task.AgentID.String
		}
	case rev.GoalID.Valid:
		goal, err := d.cfg.Queries.GetAgentGoal(ctx, rev.GoalID.String)
		if err != nil {
			return
		}
		userID = goal.UserID
		if goal.AgentID.Valid {
			executorAgentID = goal.AgentID.String
		}
	default:
		return
	}
	if executorAgentID == "" {
		d.cfg.Logger.Warn("dispatcher: reviewer no executor", "review", rev.ID)
		return
	}
	// Mint a fresh session — reviewer runs are a different conversation than
	// the worker run that produced the output.
	stub := sqlc.AgentTask{UserID: userID, AgentID: nullable(executorAgentID)}
	sessionID, err := d.cfg.NewSession(ctx, stub, executorAgentID)
	if err != nil {
		return
	}
	runID := uuid.NewString()
	nowStr := now.UTC().Format(time.RFC3339Nano)

	// Attempt number: reviewer runs share the agent_task_run table; key the
	// attempt off (task_id or goal_id) + kind=reviewer.
	var attempt int64 = 1
	if rev.TaskID.Valid {
		n, err := d.cfg.Queries.NextAttemptNoForTask(ctx, sqlc.NextAttemptNoForTaskParams{
			TaskID: rev.TaskID, Kind: RunKindReviewer,
		})
		if err == nil {
			attempt = n
		}
	} else {
		n, err := d.cfg.Queries.NextAttemptNoForGoal(ctx, sqlc.NextAttemptNoForGoalParams{
			GoalID: rev.GoalID, Kind: RunKindReviewer,
		})
		if err == nil {
			attempt = n
		}
	}

	if _, err := d.cfg.Queries.CreateAgentTaskRun(ctx, sqlc.CreateAgentTaskRunParams{
		ID:              runID,
		TaskID:          rev.TaskID,
		GoalID:          rev.GoalID,
		UserID:          userID,
		AgentID:         nullable(executorAgentID),
		ExecutorAgentID: nullable(executorAgentID),
		Kind:            RunKindReviewer,
		AttemptNo:       attempt,
		Status:          RunQueued,
		SessionID:       sessionID,
		Input:           "{}",
		LeaseExpiresAt:  sql.NullString{},
		WorkerID:        "",
		StartedAt:       sql.NullString{},
		CreatedAt:       nowStr,
		UpdatedAt:       nowStr,
	}); err != nil {
		return
	}
	if n, err := d.cfg.Queries.SetAgentReviewReviewerRun(ctx, sqlc.SetAgentReviewReviewerRunParams{
		ReviewerRunID: nullable(runID), UpdatedAt: nowStr, ID: rev.ID,
	}); err != nil || n == 0 {
		// Lost the race; another tick already attached a reviewer run. Mark
		// our orphan run failed so it doesn't leak.
		_ = d.cfg.Queries.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
			Status: RunCancelled, Result: "", Error: "lost dispatch race",
			FinishedAt: sql.NullString{String: nowStr, Valid: true},
			UpdatedAt:  nowStr, ID: runID,
		})
		return
	}
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		TaskID:    rev.TaskID,
		GoalID:    rev.GoalID,
		RunID:     nullable(runID),
		ReviewID:  nullable(rev.ID),
		EventType: "dispatch_reviewer",
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"executor_agent_id": executorAgentID}),
	})
	// Noop runner fallback for the reviewer path.
	d.failReviewerRunAsNoop(ctx, rev.ID, runID)
}

// failGoalRunAsNoop finalizes a freshly created goal run with a
// protocol_error event, mimicking the noop worker runner's behavior.
func (d *Dispatcher) failGoalRunAsNoop(ctx context.Context, goalID, runID, kind string) {
	nowStr := d.cfg.Service.now()
	_ = d.cfg.Queries.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
		Status: RunFailed, Result: "", Error: "noop runner: " + kind + " not wired",
		FinishedAt: sql.NullString{String: nowStr, Valid: true},
		UpdatedAt:  nowStr, ID: runID,
	})
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		GoalID:    nullable(goalID),
		RunID:     nullable(runID),
		EventType: "protocol_error",
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"reason": "noop_" + kind + "_runner", "kind": kind}),
	})
}

// failReviewerRunAsNoop is the reviewer-path equivalent of failGoalRunAsNoop.
// Leaves the review status in_progress so the dispatcher doesn't re-dispatch
// on the next tick; a future runner PR will replace this with a real
// decision call.
func (d *Dispatcher) failReviewerRunAsNoop(ctx context.Context, reviewID, runID string) {
	nowStr := d.cfg.Service.now()
	_ = d.cfg.Queries.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
		Status: RunFailed, Result: "", Error: "noop runner: reviewer not wired",
		FinishedAt: sql.NullString{String: nowStr, Valid: true},
		UpdatedAt:  nowStr, ID: runID,
	})
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		ReviewID:  nullable(reviewID),
		RunID:     nullable(runID),
		EventType: "protocol_error",
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"reason": "noop_reviewer_runner"}),
	})
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

// resolveGoalExecutor picks an executor for planner/synthesizer runs. Only
// the creator agent counts — there is no per-goal dispatch hint at this
// slice.
func (d *Dispatcher) resolveGoalExecutor(g sqlc.AgentGoal) (string, bool) {
	if g.AgentID.Valid && g.AgentID.String != "" {
		return g.AgentID.String, true
	}
	return "", false
}

func (d *Dispatcher) emitGoalProtocolError(ctx context.Context, goalID, reason string) {
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		GoalID:    nullable(goalID),
		EventType: "protocol_error",
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"reason": reason}),
	})
}
