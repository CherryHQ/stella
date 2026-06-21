package goal

import (
	"context"
	"errors"
	"log/slog"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// River Phase 2a: goal attempt execution runs on durable River jobs instead of
// the old in-process worker pool. Claiming a leaf mints an attempt and enqueues
// ONE job here; a worker promotes and drives it to a terminal attempt state.
// River owns durability, multi-node distribution, and graceful drain — but NOT
// retry: MaxAttempts is 1 so the same attempt_id is never re-run. Retry is the
// goal convergence model's job (attempt_count / reopenForRework), driven by the
// dispatcher reaper off the lease.
//
// The goal queue's worker runs on the single process-wide River client
// (db.NewWorkingRiverClient): there must be exactly one electable client per
// database, so the goal subsystem contributes its queue + worker to that shared
// client (RegisterGoalWorker / GoalQueueConfig) rather than building its own.
const (
	// GoalQueue isolates goal-attempt execution from the scheduler's queue on the
	// shared River client.
	GoalQueue = "stella_goal"
	// defaultGoalMaxWorkers bounds concurrent attempt executions per node when the
	// boot config does not override it (the durable successor to the old
	// in-process worker-pool default of 5).
	defaultGoalMaxWorkers = 5
)

// goalAttemptArgs is the River payload for executing one claimed attempt. The
// goal and attempt are resolved fresh from the DB at work time (Worker.Run), so
// the payload carries only their IDs.
type goalAttemptArgs struct {
	GoalID    string `json:"goal_id"`
	AttemptID string `json:"attempt_id"`
}

// Kind implements river.JobArgs.
func (goalAttemptArgs) Kind() string { return "stella_goal_attempt" }

// goalEnqueuer is the subset of the River client the dispatcher needs to enqueue
// attempt jobs. Declared so the dispatcher compiles against the boundary and
// tests can drive scan-and-claim with a fake enqueuer.
type goalEnqueuer interface {
	Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// goalAttemptWorker runs one fired attempt job by delegating to the same
// WorkerRunner the dispatcher used to call in-process. It is the only consumer
// of the goal queue.
type goalAttemptWorker struct {
	river.WorkerDefaults[goalAttemptArgs]
	runner WorkerRunner
	log    *slog.Logger
}

// Work implements river.Worker. A superseded attempt (reaped/cancelled before
// this job ran, so PromoteAttempt matches zero rows) surfaces as
// ErrInvalidTransition; that is benign — the goal already recovered — so the job
// completes cleanly rather than erroring. Any other error is returned; with
// MaxAttempts=1 River discards it and convergence (the reaper) owns recovery.
func (w *goalAttemptWorker) Work(ctx context.Context, j *river.Job[goalAttemptArgs]) error {
	err := w.runner.Run(ctx, j.Args.GoalID, j.Args.AttemptID)
	if errors.Is(err, ErrInvalidTransition) {
		w.log.Info("goal: river fired for superseded attempt, skipping",
			"goal_id", j.Args.GoalID, "attempt_id", j.Args.AttemptID)
		return nil
	}
	return err
}

// goalInsertOpts is the InsertOpts every attempt job is enqueued with: the goal
// queue, no River-level retry (MaxAttempts=1), and uniqueness by args so a
// duplicate insert for the same attempt collapses to one in-flight job.
func goalInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       GoalQueue,
		MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	}
}

// RegisterGoalWorker registers the goal-attempt worker into a shared workers
// bundle used to build the process-wide River client. maxWorkers bounds
// concurrent attempt executions per node (the durable successor to the old
// in-process worker-pool cap); the per-root/per-user caps enforced at claim still
// bound total in-flight attempts cluster-wide.
func RegisterGoalWorker(workers *river.Workers, runner WorkerRunner, log *slog.Logger) {
	river.AddWorker(workers, &goalAttemptWorker{runner: runner, log: log})
}
