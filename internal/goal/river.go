package goal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
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

	// GoalTickQueue runs the dispatcher convergence tick (River Phase 2b). It is a
	// dedicated queue with one worker per node so a slow tick never consumes
	// attempt-execution slots and never overlaps itself on a node; combined with
	// leader-only periodic enqueue and ByState uniqueness, the cluster runs a
	// single convergence loop (at most one live tick at a time) rather than one
	// scan per node. A leader failover may run one extra (idempotent) pass.
	GoalTickQueue = "stella_goal_tick"
	// goalTickTimeout bounds one cooperative (ctx-aware) tick so a slow scan does
	// not hold the single tick worker indefinitely and starve future ticks while
	// ByState uniqueness blocks them; the next periodic fire re-runs convergence
	// once it frees. Generous relative to a BatchLimit-bounded scan. (Tick honors
	// ctx via its DB calls, so the timeout cancels in-flight work cooperatively.)
	goalTickTimeout = 5 * time.Minute
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
// attempt jobs. InsertTx enqueues the attempt job inside the claim's own
// transaction so claim+enqueue commit atomically (River Phase 2c): a crash
// between claiming and enqueuing can no longer strand a claimed attempt with no
// job. Declared as an interface so the dispatcher compiles against the boundary
// and tests can drive scan-and-claim with a fake enqueuer. *river.Client[pgx.Tx]
// satisfies it.
type goalEnqueuer interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// AttemptEnqueuer enqueues the durable execution job for a freshly claimed
// attempt within the claim transaction tx, so the claim and its job are atomic
// (River Phase 2c). GoalService.Claim calls it after minting the attempt;
// returning an error rolls the whole claim back. A nil AttemptEnqueuer skips
// enqueue (tests that mint+claim directly without River).
type AttemptEnqueuer func(ctx context.Context, tx pgx.Tx, goalID, attemptID string) error

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

// goalTickArgs is the River payload for one convergence tick. It carries no
// fields: the tick acts on whatever the DB scan finds, and its empty (constant)
// args make ByState uniqueness collapse all pending ticks to one in-flight job.
type goalTickArgs struct{}

// Kind implements river.JobArgs.
func (goalTickArgs) Kind() string { return "stella_goal_tick" }

// goalTickWorker runs one dispatcher convergence pass. The dispatcher's own
// per-tick guards (isStopped, lease-based reap) make a fired tick safe whether it
// runs on the leader or any other node.
type goalTickWorker struct {
	river.WorkerDefaults[goalTickArgs]
	dispatcher *Dispatcher
	log        *slog.Logger
}

// Timeout bounds a single tick (the client default is no timeout). See
// goalTickTimeout.
func (w *goalTickWorker) Timeout(*river.Job[goalTickArgs]) time.Duration { return goalTickTimeout }

// Work implements river.Worker. A tick never fails the job: the convergence pass
// logs its own per-step errors and the next periodic fire retries, so there is
// nothing for River to retry.
func (w *goalTickWorker) Work(ctx context.Context, _ *river.Job[goalTickArgs]) error {
	w.dispatcher.Tick(ctx)
	return nil
}

// RegisterGoalTickWorker registers the convergence-tick worker into the shared
// workers bundle (River Phase 2b). Paired with GoalTickQueueConfig and the
// periodic registered by Service.StartDispatchTick.
func RegisterGoalTickWorker(workers *river.Workers, d *Dispatcher, log *slog.Logger) {
	river.AddWorker(workers, &goalTickWorker{dispatcher: d, log: log})
}

// goalTickInsertOpts is the InsertOpts the tick periodic enqueues with: the tick
// queue, no River-level retry, and ByState uniqueness so a new tick is skipped
// while one is still available/pending/running/scheduled. That restores the old
// "skip the tick if the previous one is still in flight" behavior and bounds the
// queue to a single live tick cluster-wide.
func goalTickInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       GoalTickQueue,
		MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	}
}
