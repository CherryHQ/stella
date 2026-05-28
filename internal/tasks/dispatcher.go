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

// SchedulerLike is the subset of scheduler.Service Service.Start needs to
// register the dispatcher tick. Tests can pass a stub or drive Tick directly.
type SchedulerLike interface {
	ScheduleEvery(ctx context.Context, every string, fn func(ctx context.Context)) error
}

// ExecutorResolver decides which settings_agent should execute a given run.
// Resolution per D13: live dispatch hint -> session-derived -> creator
// fallback. The function may return ("", false) if it cannot resolve; the
// dispatcher will refuse the claim and emit a protocol_error event.
type ExecutorResolver func(ctx context.Context, task sqlc.AgentTask) (agentID string, ok bool)

// SessionMinter returns a fresh session id for a first-run dispatch (i.e.
// task.session_id IS NULL). It must produce a unique id per call. In
// production this is wired to memory.Provider.NewSession; in tests it can be
// a simple uuid generator.
type SessionMinter func(ctx context.Context, task sqlc.AgentTask) (string, error)

// DispatcherConfig holds the wiring for a Dispatcher.
type DispatcherConfig struct {
	Service    *TransitionService
	Queries    *sqlc.Queries
	Runner     RunnerFunc
	Resolver   ExecutorResolver
	NewSession SessionMinter
	TickEvery  time.Duration // 0 => 2s
	MaxPerOrg  int           // 0 => 5
	LeaseTTL   time.Duration // 0 => LeaseDuration
	BatchLimit int           // 0 => 50
	Logger     *slog.Logger
}

// Dispatcher drives the task system: it interrupts stale runs, propagates dep
// failures, scans dispatchable tasks, claims them, and spawns workers.
type Dispatcher struct {
	cfg DispatcherConfig

	mu      sync.Mutex
	running map[string]int // org_id -> live workers
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
	if cfg.MaxPerOrg == 0 {
		cfg.MaxPerOrg = 5
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
		cfg:     cfg,
		running: make(map[string]int),
		stopCh:  make(chan struct{}),
	}
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
		// InterruptStaleRun finalises the run as 'interrupted' and emits a
		// run_interrupted / run_interrupt_retry event so the audit trail
		// distinguishes lease expiry from an explicit failure (M11).
		if err := d.cfg.Service.InterruptStaleRun(ctx, InterruptStaleRunParams{
			TaskID: taskID, RunID: r.ID,
			Reason: "lease expired", Actor: SystemActor(),
		}); err != nil && !errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: interrupt stale run", "task", taskID, "err", err)
		}
	}
}

// propagateDepFailures scans tasks whose hard dep has failed (or been
// cancelled) without a waiver and either blocks them or fails them per
// on_failure.
//
// Scope (M9): only ready candidates are scanned. If an upstream fails while
// a downstream is already running, on_failure=block / on_failure=fail will
// NOT fire mid-run; the downstream worker is expected to notice through its
// own logic (or run to completion). This is intentional — interrupting a
// running task on remote state change widens the race surface and makes
// worker semantics unpredictable.
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
		if !d.underOrgCap(task.OrgID) {
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
		execID, ok := d.resolveExecutor(ctx, task)
		if !ok {
			d.emitProtocolError(ctx, task.ID, "no executor resolved")
			continue
		}
		sessionID, err := d.cfg.NewSession(ctx, task)
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: mint session", "task", task.ID, "err", err)
			continue
		}
		res, err := d.cfg.Service.Claim(ctx, ClaimParams{
			TaskID: task.ID, ExecutorAgentID: execID,
			WorkerID: "", LeaseDuration: d.cfg.LeaseTTL,
			NewSessionID: sessionID, Actor: SystemActor(),
		})
		if errors.Is(err, ErrInvalidTransition) {
			continue // lost the race
		}
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: claim", "task", task.ID, "err", err)
			continue
		}
		d.spawnWorker(ctx, task.OrgID, task.ID, res.RunID)
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

func (d *Dispatcher) resolveExecutor(ctx context.Context, task sqlc.AgentTask) (string, bool) {
	// 1) Live dispatch hint.
	hint, err := d.cfg.Queries.GetLiveDispatchHintForTask(ctx, sqlc.GetLiveDispatchHintForTaskParams{
		TaskID: task.ID, Kind: RunKindWorker,
	})
	if err == nil && hint.ExecutorAgentID != "" {
		return hint.ExecutorAgentID, true
	}
	// 2) Caller-supplied resolver (session/creator chain).
	if d.cfg.Resolver != nil {
		if a, ok := d.cfg.Resolver(ctx, task); ok {
			return a, true
		}
	}
	// 3) Creator fallback.
	if task.AgentID.Valid && task.AgentID.String != "" {
		return task.AgentID.String, true
	}
	return "", false
}

func (d *Dispatcher) emitProtocolError(ctx context.Context, taskID, reason string) {
	_ = d.cfg.Service.appendEvent(ctx, d.cfg.Queries, sqlc.InsertAgentTaskEventParams{
		TaskID:    nullable(taskID),
		EventType: "protocol_error",
		ActorType: ActorSystem,
		Detail:    detailJSON(map[string]any{"reason": reason}),
	})
}

func (d *Dispatcher) underOrgCap(orgID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running[orgID] < d.cfg.MaxPerOrg
}

func (d *Dispatcher) incOrg(orgID string) {
	d.mu.Lock()
	d.running[orgID]++
	d.mu.Unlock()
}

func (d *Dispatcher) decOrg(orgID string) {
	d.mu.Lock()
	d.running[orgID]--
	if d.running[orgID] <= 0 {
		delete(d.running, orgID)
	}
	d.mu.Unlock()
}

func (d *Dispatcher) spawnWorker(ctx context.Context, orgID, taskID, runID string) {
	d.incOrg(orgID)
	d.wg.Go(func() {
		defer d.decOrg(orgID)
		w := NewWorker(d.cfg.Service, d.cfg.Queries, d.cfg.Runner)
		if err := w.Run(ctx, taskID, runID, SystemActor()); err != nil {
			d.cfg.Logger.Warn("dispatcher: worker returned error", "task", taskID, "err", err)
		}
	})
}

// WaitIdle blocks until no workers are in flight. Useful for tests.
func (d *Dispatcher) WaitIdle() { d.wg.Wait() }
