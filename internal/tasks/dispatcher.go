package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SchedulerLike is the subset of scheduler.Service the dispatcher needs.
// Allows tests to drive ticks directly without wiring gocron.
type SchedulerLike interface {
	ScheduleEvery(ctx context.Context, every string, fn func(ctx context.Context)) error
}

// ExecutorResolver decides which agent should execute a given run.
// Resolution per D13: live dispatch hint -> latest-run executor -> owner agent
// fallback. The function may return ("", false) if it cannot resolve; the
// dispatcher will refuse the claim and emit a protocol_error event.
type ExecutorResolver func(ctx context.Context, task sqlc.AgentTask) (agentID string, ok bool)

// SessionMinter returns a fresh durable worker session id for a task. It must
// produce a unique id per call and scope the session to the resolved agent and
// optional project context.
type SessionMinter func(ctx context.Context, userID, agentID, projectID string) (string, error)

// DispatcherConfig holds the wiring for a Dispatcher.
type DispatcherConfig struct {
	Service    *TransitionService
	Queries    *sqlc.Queries
	Executor   Executor
	Resolver   ExecutorResolver
	NewSession SessionMinter
	TickEvery  time.Duration // 0 => 2s
	MaxWorkers int           // 0 => 5
	LeaseTTL   time.Duration // 0 => LeaseDuration
	BatchLimit int           // 0 => 50
	Logger     *slog.Logger
}

// Dispatcher drives the task system: it interrupts stale runs, propagates dep
// failures, scans dispatchable tasks, claims them, and spawns workers.
type Dispatcher struct {
	cfg DispatcherConfig

	mu      sync.Mutex
	running int // live worker count
	wg      sync.WaitGroup

	stopCh  chan struct{}
	stopped bool
}

// NewDispatcher constructs a dispatcher. Defaults applied for zero-valued
// config fields.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.TickEvery == 0 {
		cfg.TickEvery = 2 * time.Second
	}
	if cfg.MaxWorkers == 0 {
		cfg.MaxWorkers = 5
	}
	if cfg.LeaseTTL == 0 {
		cfg.LeaseTTL = LeaseDuration
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = 50
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default().With("component", "tasks/dispatcher")
	}
	return &Dispatcher{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// Start registers the dispatcher tick on the given scheduler. If sched is nil
// the dispatcher is silent — callers must call Tick directly (used by tests).
func (d *Dispatcher) Start(ctx context.Context, sched SchedulerLike) error {
	if sched == nil {
		return nil
	}
	return sched.ScheduleEvery(ctx, fmt.Sprintf("%ds", int(d.cfg.TickEvery.Seconds())), func(ctx context.Context) {
		d.Tick(ctx)
	})
}

// Stop waits for all in-flight workers to drain. The dispatcher tick stops
// firing when its scheduler is stopped (the scheduler owns the lifecycle).
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	close(d.stopCh)
	d.mu.Unlock()
	d.wg.Wait()
}

// Tick runs one pass of the dispatch loop. Public so tests can drive it
// deterministically.
func (d *Dispatcher) Tick(ctx context.Context) {
	if d.isStopped() {
		return
	}
	now := d.cfg.Service.clock().UTC()
	d.interruptStaleRuns(ctx, now)
	d.propagateDepFailures(ctx, now)
	d.rollupGoals(ctx, now)
	d.scanAndDispatch(ctx, now)
}

func (d *Dispatcher) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// interruptStaleRuns finds queued/running runs whose lease has expired and
// marks them interrupted, returning their tasks to ready if the retry budget
// allows.
func (d *Dispatcher) interruptStaleRuns(ctx context.Context, now time.Time) {
	stale, err := d.cfg.Queries.ListStaleAgentTaskRuns(ctx, sqlc.ListStaleAgentTaskRunsParams{
		LeaseExpiresAt: sql.NullString{String: now.Format(time.RFC3339Nano), Valid: true},
		Limit:          int64(d.cfg.BatchLimit),
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list stale runs", "err", err)
		return
	}
	for _, r := range stale {
		taskID := ""
		if r.TaskID.Valid {
			taskID = r.TaskID.String
		}
		if taskID == "" {
			continue
		}
		// Fail() finalizes the run row; override the final status to
		// RunInterrupted so the audit trail records this as a lease expiry
		// rather than an explicit failure.
		if err := d.cfg.Service.Fail(ctx, FailParams{
			TaskID: taskID, RunID: r.ID,
			Reason: "lease expired", Retryable: true,
			RunStatusOnFail: RunInterrupted, Actor: SystemActor(),
		}); err != nil && !errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: fail-after-interrupt", "task", taskID, "err", err)
		}
	}
}

// propagateDepFailures scans tasks whose hard dep has failed (or been
// cancelled) without a waiver and either blocks them or fails them per
// on_failure.
func (d *Dispatcher) propagateDepFailures(ctx context.Context, now time.Time) {
	candidates, err := d.cfg.Queries.ListReadyCandidates(ctx, sqlc.ListReadyCandidatesParams{
		NotBefore: sql.NullString{String: now.Format(time.RFC3339Nano), Valid: true},
		Limit:     int64(d.cfg.BatchLimit),
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list candidates", "err", err)
		return
	}
	for _, task := range candidates {
		deps, err := d.cfg.Queries.ListAgentTaskDepsWithUpstream(ctx, task.ID)
		if err != nil {
			continue
		}
		for _, row := range deps {
			edge := row.AgentTaskDep
			if edge.DepKind != DepKindHard {
				continue
			}
			if row.UpstreamStatus != StatusFailed && row.UpstreamStatus != StatusCancelled {
				continue
			}
			if edge.WaivedAt.Valid {
				continue
			}
			switch edge.OnFailure {
			case OnFailureBlock:
				_ = d.blockOnDepFailure(ctx, task.ID, edge.DepTaskID, row.UpstreamStatus)
			case OnFailureFail:
				_ = d.failOnDepPropagation(ctx, task.ID, edge.DepTaskID, row.UpstreamStatus)
			}
			break // one trigger per task per tick
		}
	}
}

// blockOnDepFailure transitions a task to blocked with kind=dep_failure.
func (d *Dispatcher) blockOnDepFailure(ctx context.Context, taskID, depID, upstreamStatus string) error {
	return d.cfg.Service.Block(ctx, BlockParams{
		TaskID: taskID, Kind: BlockerKindDepFailure,
		Question: fmt.Sprintf("upstream %s is %s", depID, upstreamStatus),
		Detail:   detailJSON(map[string]any{"dep_task_id": depID, "upstream_status": upstreamStatus}),
		Actor:    SystemActor(),
	})
}

// failOnDepPropagation transitions a task to failed because its dep failed
// with on_failure=fail.
func (d *Dispatcher) failOnDepPropagation(ctx context.Context, taskID, depID, upstreamStatus string) error {
	return d.cfg.Service.Fail(ctx, FailParams{
		TaskID: taskID, Reason: fmt.Sprintf("dep %s %s, on_failure=fail", depID, upstreamStatus),
		Retryable: false, Actor: SystemActor(),
	})
}

// scanAndDispatch picks ready candidates, runs readiness.Compute, resolves
// executor, claims, and spawns a worker.
func (d *Dispatcher) scanAndDispatch(ctx context.Context, now time.Time) {
	candidates, err := d.cfg.Queries.ListReadyCandidates(ctx, sqlc.ListReadyCandidatesParams{
		NotBefore: sql.NullString{String: now.Format(time.RFC3339Nano), Valid: true},
		Limit:     int64(d.cfg.BatchLimit),
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list candidates", "err", err)
		return
	}
	for _, task := range candidates {
		if d.isStopped() {
			return
		}
		if !d.underCap() {
			continue
		}
		depViews, err := d.loadDepViews(ctx, task.ID)
		if err != nil {
			continue
		}
		r := Compute(task, depViews, now)
		if !r.Dispatchable {
			continue
		}
		execID, hintID, ok := d.resolveExecutor(ctx, task)
		if !ok {
			d.emitProtocolError(ctx, task.ID, "no executor resolved")
			continue
		}
		sessionID := task.SessionID
		if sessionID == "" {
			d.emitProtocolError(ctx, task.ID, "task has no worker session")
			continue
		}
		res, err := d.cfg.Service.Claim(ctx, ClaimParams{
			TaskID: task.ID, ExecutorAgentID: execID,
			WorkerID: "", LeaseDuration: d.cfg.LeaseTTL,
			Actor: SystemActor(), HintID: hintID,
		})
		if errors.Is(err, ErrInvalidTransition) {
			continue // lost the race
		}
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: claim", "task", task.ID, "err", err)
			continue
		}
		d.spawnWorker(ctx, task.ID, res.RunID)
	}
}

func (d *Dispatcher) loadDepViews(ctx context.Context, taskID string) ([]DepEdgeView, error) {
	rows, err := d.cfg.Queries.ListAgentTaskDepsWithUpstream(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]DepEdgeView, 0, len(rows))
	for _, r := range rows {
		out = append(out, DepEdgeView{
			DepTaskID:      r.AgentTaskDep.DepTaskID,
			Kind:           r.AgentTaskDep.DepKind,
			OnFailure:      r.AgentTaskDep.OnFailure,
			Waived:         r.AgentTaskDep.WaivedAt.Valid,
			UpstreamStatus: r.UpstreamStatus,
		})
	}
	return out, nil
}

// resolveExecutor returns the executor agent ID for a dispatchable task plus
// the hint ID that produced it (empty when not resolved via hint). Claim
// consumes the exact hint ID inside its tx so a concurrent hint replacement
// can't make the resolved executor and the consumed hint diverge.
func (d *Dispatcher) resolveExecutor(ctx context.Context, task sqlc.AgentTask) (string, string, bool) {
	// 1) Live dispatch hint.
	hint, err := d.cfg.Queries.GetLiveDispatchHintForTask(ctx, sqlc.GetLiveDispatchHintForTaskParams{
		TaskID: nullable(task.ID), Kind: RunKindWorker,
	})
	if err == nil && hint.ExecutorAgentID != "" {
		return hint.ExecutorAgentID, hint.ID, true
	}
	// 2) Caller-supplied resolver (latest-run executor preservation).
	if d.cfg.Resolver != nil {
		if a, ok := d.cfg.Resolver(ctx, task); ok {
			return a, "", true
		}
	}
	// 3) Owner/manager agent fallback.
	if task.AgentID != "" {
		return task.AgentID, "", true
	}
	return "", "", false
}

func (d *Dispatcher) emitProtocolError(ctx context.Context, taskID, reason string) {
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		TaskID:    nullable(taskID),
		EventType: "protocol_error",
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"reason": reason}),
	})
}

func (d *Dispatcher) underCap() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running < d.cfg.MaxWorkers
}

func (d *Dispatcher) inc() {
	d.mu.Lock()
	d.running++
	d.mu.Unlock()
}

func (d *Dispatcher) dec() {
	d.mu.Lock()
	d.running--
	d.mu.Unlock()
}

func (d *Dispatcher) spawnWorker(ctx context.Context, taskID, runID string) {
	d.inc()
	d.wg.Go(func() {
		defer d.dec()
		w := NewWorker(d.cfg.Service, d.cfg.Queries, d.cfg.Executor)
		if err := w.Run(ctx, taskID, runID, SystemActor()); err != nil {
			d.cfg.Logger.Warn("dispatcher: worker returned error", "task", taskID, "err", err)
		}
	})
}

// WaitIdle blocks until no workers are in flight. Useful for tests.
func (d *Dispatcher) WaitIdle() { d.wg.Wait() }
