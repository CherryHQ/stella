package goal

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

// WorkerRunner drives one claimed attempt to completion: promote → execute →
// run checks → apply the single fold transition. The dispatcher owns the
// concurrency budget and lifetime; the worker (worker.go, integration phase)
// owns the per-attempt machinery. Declared here so the dispatcher compiles
// against the boundary without depending on the worker implementation — the
// same Executor/CheckRunner-style decoupling the spine uses for the service's
// collaborators.
type WorkerRunner interface {
	// Run executes the given (already-claimed, queued) attempt to a terminal
	// attempt state, folding acceptance through the service. It must be safe to
	// call concurrently for distinct attempts.
	Run(ctx context.Context, goalID, attemptID string) error
}

// SchedulerLike is the subset of the scheduler the dispatcher needs. It lets
// tests drive Tick directly without wiring a real scheduler (carried verbatim
// from the old tasks dispatcher).
type SchedulerLike interface {
	ScheduleEvery(ctx context.Context, every string, fn func(ctx context.Context)) error
}

// DispatcherConfig wires a Dispatcher. Zero-valued fields fall back to the
// package defaults below.
type DispatcherConfig struct {
	Service *GoalService
	Queries *sqlc.Queries
	Worker  WorkerRunner

	TickEvery time.Duration // 0 ⇒ defaultTickEvery
	// MaxWorkers caps attempts this process executes concurrently (the in-process
	// worker-pool bound; distinct from the durable per-root/per-user caps below).
	MaxWorkers int           // 0 ⇒ defaultMaxWorkers
	LeaseTTL   time.Duration // 0 ⇒ service LeaseTTL
	BatchLimit int           // 0 ⇒ defaultBatchLimit
	// MaxConcurrentPerUser caps in-flight attempts per user (§5/§10.8, default
	// 16). 0 ⇒ the service's configured per-user cap.
	MaxConcurrentPerUser int
	Logger               *slog.Logger
}

const (
	defaultTickEvery  = 2 * time.Second
	defaultMaxWorkers = 5
	defaultBatchLimit = 50
)

// Dispatcher drives the convergence loop on a tick: reap stale attempts,
// propagate dep failures, roll up composites, then scan-and-claim dispatchable
// leaves under the concurrency budget and spawn a bounded pool of workers
// (contract §5, §7). It is the only scheduler; every durable write still routes
// through GoalService.
type Dispatcher struct {
	cfg DispatcherConfig

	mu sync.Mutex
	// active is the set of attempt IDs this process is executing right now. A
	// member must never be reaped as "lease expired": the worker is alive even if
	// its heartbeat lost the contended SQLite writer. The DB lease is the
	// crash/restart backstop (an empty set after restart reclaims orphans).
	// Single-process only — multi-replica reclaim keys off per-replica liveness.
	active  map[string]bool
	wg      sync.WaitGroup
	stopCh  chan struct{}
	stopped bool
}

// NewDispatcher constructs a dispatcher, filling defaults for zero-valued
// config fields.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.TickEvery == 0 {
		cfg.TickEvery = defaultTickEvery
	}
	if cfg.MaxWorkers == 0 {
		cfg.MaxWorkers = defaultMaxWorkers
	}
	if cfg.LeaseTTL == 0 && cfg.Service != nil {
		cfg.LeaseTTL = cfg.Service.cfg.LeaseTTL
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = defaultBatchLimit
	}
	if cfg.MaxConcurrentPerUser == 0 && cfg.Service != nil {
		cfg.MaxConcurrentPerUser = cfg.Service.cfg.MaxConcurrentPerUser
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default().With("component", "goal/dispatcher")
	}
	return &Dispatcher{
		cfg:    cfg,
		active: map[string]bool{},
		stopCh: make(chan struct{}),
	}
}

// Start registers the tick on sched. A nil scheduler is silent: callers drive
// Tick directly (tests).
func (d *Dispatcher) Start(ctx context.Context, sched SchedulerLike) error {
	if sched == nil {
		return nil
	}
	return sched.ScheduleEvery(ctx, fmt.Sprintf("%ds", int(d.cfg.TickEvery.Seconds())), func(ctx context.Context) {
		d.Tick(ctx)
	})
}

// Stop signals the tick to go quiet and drains in-flight workers.
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
// deterministically. The order is fixed (§5/§7): reap → propagate → rollup →
// scan-and-claim.
func (d *Dispatcher) Tick(ctx context.Context) {
	if d.isStopped() {
		return
	}
	now := d.cfg.Service.clock().UTC()
	d.reapStaleAttempts(ctx, now)
	d.propagateDepFailures(ctx, now)
	d.rollupComposites(ctx, now)
	d.scanAndClaim(ctx, now)
}

func (d *Dispatcher) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// reapStaleAttempts finds queued/running attempts whose lease has expired and
// (unless this process is actively executing them) finalizes them as
// 'interrupted' through the service, which returns the goal to ready
// within its convergence budget (contract §2.2).
func (d *Dispatcher) reapStaleAttempts(ctx context.Context, now time.Time) {
	stale, err := d.cfg.Queries.ListStaleAttempts(ctx, sqlc.ListStaleAttemptsParams{
		Now:   sql.NullTime{Time: now, Valid: true},
		Limit: int32(d.cfg.BatchLimit),
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list stale attempts", "err", err)
		return
	}
	for _, a := range stale {
		// Never reap an attempt this process is actively executing: the worker is
		// alive even if its heartbeat lost the contended writer. Only genuinely
		// orphaned attempts (never started here, or after a crash/restart) reclaim.
		if d.isActive(a.ID) {
			continue
		}
		if err := d.cfg.Service.ReapAttempt(ctx, a.ID); err != nil && !errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: reap attempt", "attempt", a.ID, "goal", a.GoalID, "err", err)
		}
	}
}

// propagateDepFailures scans dispatchable-leaf candidates and blocks any whose
// hard upstream edge is terminal-bad (rejected_final/abandoned/cancelled),
// unwaived, with on_failure=block. Readiness.Compute already derives this
// verdict from the pre-joined upstream state, so the dispatcher only applies the
// service transition (contract §2.1 ready/active→blocked(dep)).
func (d *Dispatcher) propagateDepFailures(ctx context.Context, now time.Time) {
	candidates, err := d.cfg.Queries.ListDispatchableLeaves(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list candidates for dep propagation", "err", err)
		return
	}
	for _, goal := range candidates {
		if d.isStopped() {
			return
		}
		edges, err := d.cfg.Queries.ListEdgeWithUpstreamState(ctx, goal.ID)
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: list edges", "goal", goal.ID, "err", err)
			continue
		}
		r := Compute(goal, edges, now)
		if r.State != ReadinessBlocked {
			continue
		}
		if err := d.cfg.Service.Block(ctx, goal.ID, BlockDep, SystemActor()); err != nil &&
			!errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: block on dep failure", "goal", goal.ID, "err", err)
		}
	}
}

// rollupComposites drives parent acceptance off the incremental counters. The
// accept-ready scan (ListRollupCandidates) and the stalled scan
// (ListStalledComposites) feed RollupComposite (pure); the service applies the
// single parent transition. A genuinely stalled parent (RollupWait but
// required_accepted < required_total) triggers the reconcileCounters backstop
// (contract §6).
func (d *Dispatcher) rollupComposites(ctx context.Context, _ time.Time) {
	ready, err := d.cfg.Queries.ListRollupCandidates(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list rollup candidates", "err", err)
	} else {
		for _, parent := range ready {
			d.applyRollup(ctx, parent, false)
		}
	}

	stalled, err := d.cfg.Queries.ListStalledComposites(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list stalled composites", "err", err)
		return
	}
	for _, parent := range stalled {
		d.applyRollup(ctx, parent, true)
	}
}

// applyRollup folds one composite's counters into a verdict and applies it. On a
// stalled parent that the verdict cannot move (RollupWait), it runs the
// reconcileCounters backstop — never per event, only on detected stall.
func (d *Dispatcher) applyRollup(ctx context.Context, parent sqlc.AgentGoal, stalled bool) {
	switch RollupComposite(parent) {
	case RollupAcceptParent:
		if err := d.cfg.Service.RollupAccept(ctx, parent.ID); err != nil &&
			!errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: rollup accept", "goal", parent.ID, "err", err)
		}
	case RollupBlock:
		if err := d.cfg.Service.Block(ctx, parent.ID, BlockDep, SystemActor()); err != nil &&
			!errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: rollup block", "goal", parent.ID, "err", err)
		}
	case RollupFail:
		if err := d.cfg.Service.RollupFail(ctx, parent.ID); err != nil &&
			!errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: rollup fail", "goal", parent.ID, "err", err)
		}
	case RollupWait:
		if stalled {
			if err := d.cfg.Service.reconcileCounters(ctx, parent.ID); err != nil {
				d.cfg.Logger.Warn("dispatcher: reconcile counters", "goal", parent.ID, "err", err)
			}
		}
	}
}

// scanAndClaim picks dispatchable leaves, enforces readiness + the per-root and
// per-user concurrency caps (§5/§10.8), resolves the executor, claims through
// the service, and spawns a bounded worker per claim.
func (d *Dispatcher) scanAndClaim(ctx context.Context, now time.Time) {
	candidates, err := d.cfg.Queries.ListDispatchableLeaves(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list dispatchable leaves", "err", err)
		return
	}
	// Per-tick concurrency snapshot: a fresh claim raises the in-flight count, so
	// cache counts per root/user and increment locally to honor the cap across a
	// burst of sibling claims within one tick.
	rootInflight := map[string]int64{}
	userInflight := map[string]int64{}

	for _, goal := range candidates {
		if d.isStopped() {
			return
		}
		if !d.underWorkerCap() {
			return // process worker pool full; try again next tick
		}

		edges, err := d.cfg.Queries.ListEdgeWithUpstreamState(ctx, goal.ID)
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: list edges", "goal", goal.ID, "err", err)
			continue
		}
		if r := Compute(goal, edges, now); !r.Dispatchable {
			continue
		}

		ok, err := d.underConcurrencyCap(ctx, goal, rootInflight, userInflight)
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: concurrency count", "goal", goal.ID, "err", err)
			continue
		}
		if !ok {
			continue // over the per-root or per-user budget; skip this candidate
		}

		execID, ok := d.resolveExecutor(ctx, goal)
		if !ok {
			d.cfg.Logger.Warn("dispatcher: no executor resolved", "goal", goal.ID)
			continue
		}

		attempt, err := d.cfg.Service.Claim(ctx, goal.ID, execID)
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrConcurrencyCap) {
			continue // lost the race or the service-side cap fired
		}
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: claim", "goal", goal.ID, "err", err)
			continue
		}
		rootInflight[goal.RootID]++
		userInflight[goal.UserID]++
		d.spawnWorker(ctx, goal.ID, attempt.ID)
	}
}

// underConcurrencyCap reports whether claiming goal stays within both the
// per-root cap (root policy max_concurrent, default 8) and the per-user cap
// (config, default 16). Counts are loaded once per root/user per tick and bumped
// locally as siblings are claimed, so a burst within one tick still respects the
// caps (contract §5 step 2, §10.8).
func (d *Dispatcher) underConcurrencyCap(ctx context.Context, goal sqlc.AgentGoal, rootInflight, userInflight map[string]int64) (bool, error) {
	rc, ok := rootInflight[goal.RootID]
	if !ok {
		n, err := d.cfg.Queries.CountInflightAttemptsByRoot(ctx, goal.RootID)
		if err != nil {
			return false, err
		}
		rc = n
		rootInflight[goal.RootID] = rc
	}
	if rc >= d.rootCap(ctx, goal.RootID) {
		return false, nil
	}

	uc, ok := userInflight[goal.UserID]
	if !ok {
		n, err := d.cfg.Queries.CountInflightAttemptsByUser(ctx, goal.UserID)
		if err != nil {
			return false, err
		}
		uc = n
		userInflight[goal.UserID] = uc
	}
	return uc < int64(d.cfg.MaxConcurrentPerUser), nil
}

// rootCap returns the per-root in-flight cap from the root goal's
// convergence policy (max_concurrent, default 8). A missing/unparseable policy
// falls back to the default.
func (d *Dispatcher) rootCap(ctx context.Context, rootID string) int64 {
	root, err := d.cfg.Queries.GetGoal(ctx, rootID)
	if err != nil {
		return int64(defaultMaxConcurrent)
	}
	var p ConvergencePolicy
	if err := unmarshalJSON(root.ConvergencePolicy, &p); err != nil {
		return int64(defaultMaxConcurrent)
	}
	return int64(p.Normalized().MaxConcurrent)
}

// resolveExecutor picks the executor agent for a dispatchable leaf: a live
// dispatch hint, else the last attempt's executor, else the owner agent
// (contract §2.1 "executor resolved"; carries the old D13 hint→last-run→owner
// chain). The hint is consumed transactionally by Claim, not here.
func (d *Dispatcher) resolveExecutor(ctx context.Context, goal sqlc.AgentGoal) (string, bool) {
	// 1) Live dispatch hint.
	var hint dispatchHint
	if err := unmarshalJSON(goal.DispatchHint, &hint); err == nil &&
		hint.ExecutorAgentID != "" && hint.ConsumedAt == "" {
		return hint.ExecutorAgentID, true
	}
	// 2) Last attempt's resolved executor (preserve the executor across reattempts).
	attempts, err := d.cfg.Queries.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{
		GoalID: goal.ID,
	})
	if err == nil {
		for _, a := range attempts { // already ordered attempt_no DESC
			if a.ExecutorAgentID.Valid && a.ExecutorAgentID.String != "" {
				return a.ExecutorAgentID.String, true
			}
		}
	}
	// 3) Owner agent fallback.
	if goal.AgentID != "" {
		return goal.AgentID, true
	}
	return "", false
}

// dispatchHint is the parsed shape of agent_goal.dispatch_hint
// (contract §1.1: {executor_agent_id, consumed_at}).
type dispatchHint struct {
	ExecutorAgentID string `json:"executor_agent_id"`
	ConsumedAt      string `json:"consumed_at"`
}

func (d *Dispatcher) underWorkerCap() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.active) < d.cfg.MaxWorkers
}

func (d *Dispatcher) inc(attemptID string) {
	d.mu.Lock()
	d.active[attemptID] = true
	d.mu.Unlock()
}

func (d *Dispatcher) dec(attemptID string) {
	d.mu.Lock()
	delete(d.active, attemptID)
	d.mu.Unlock()
}

func (d *Dispatcher) isActive(attemptID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active[attemptID]
}

// spawnWorker runs one claimed attempt in a tracked goroutine, bounded by the
// MaxWorkers gate the caller already checked.
func (d *Dispatcher) spawnWorker(ctx context.Context, goalID, attemptID string) {
	d.inc(attemptID)
	d.wg.Go(func() {
		defer d.dec(attemptID)
		if err := d.cfg.Worker.Run(ctx, goalID, attemptID); err != nil {
			d.cfg.Logger.Warn("dispatcher: worker returned error", "goal", goalID, "attempt", attemptID, "err", err)
		}
	})
}

// WaitIdle blocks until no workers are in flight (tests).
func (d *Dispatcher) WaitIdle() { d.wg.Wait() }

// ── Dispatcher-driven service transitions ───────────────────────────────────
//
// These are the durable writes the dispatcher tick invokes; their bodies land in
// the integration phase (converge.go) alongside applyAcceptance, so the
// single-writer fold/transition logic lives in one place. Declared here because
// the dispatcher is their only caller; signatures frozen.

// ReapAttempt finalizes a stale (lease-expired) attempt as 'interrupted' and
// returns its goal to ready within the convergence budget, or
// budget-blocks it if exhausted — one tx (contract §2.2 running→interrupted).
// Idempotent: an attempt no longer queued/running is a no-op (FinalizeAttempt
// affects 0 rows), surfacing as ErrInvalidTransition the dispatcher ignores.
func (s *GoalService) ReapAttempt(ctx context.Context, attemptID string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// Finalize the stale attempt as interrupted. A 0-row update means it is no
		// longer queued/running (already finalized/raced) — idempotent no-op the
		// dispatcher ignores as ErrInvalidTransition.
		rows, err := q.FinalizeAttempt(ctx, sqlc.FinalizeAttemptParams{
			ToStatus: AttemptInterrupted,
			Error:    "lease expired; reaped by dispatcher",
			ID:       attemptID,
		})
		if err != nil {
			return fmt.Errorf("finalize stale attempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}

		d, err := getGoal(ctx, q, att.GoalID)
		if err != nil {
			return err
		}
		// A decomposition attempt's goal is 'active' running the plan; on reap
		// just release it back to draft so a new BeginDecomposition can re-mint.
		if att.Purpose == PurposeDecomposition {
			if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
				return fmt.Errorf("clear active attempt: %w", err)
			}
			rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
				ToLifecycle:   LifecycleDraft,
				BlockReason:   "",
				ID:            d.ID,
				FromLifecycle: LifecycleActive,
			})
			if err != nil {
				return fmt.Errorf("reap decomposition: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
			return nil
		}

		// Execution attempt: an interrupt is transient. Return to ready within budget
		// so the next tick re-claims; budget out parks at blocked(budget_exhausted).
		var pol ConvergencePolicy
		_ = unmarshalJSON(d.ConvergencePolicy, &pol)
		pol = pol.Normalized()
		if d.AttemptCount < int64(pol.MaxAttempts) {
			return s.reopenForRework(ctx, q, d)
		}
		return s.blockBudget(ctx, q, d)
	})
}

// RollupAccept runs a composite parent's own Accept gate once all required
// children are accepted (contract §6, RollupComposite ⇒ accept_parent). A
// trivial-contract composite accepts immediately; an authored contract folds via
// applyAcceptance. One tx; bumps this parent's own parent counter.
func (s *GoalService) RollupAccept(ctx context.Context, id string) error {
	d, err := getGoal(ctx, s.q, id)
	if err != nil {
		return err
	}
	var contract AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &contract)
	// An authored composite contract (a synthesizer judgment item) is decided by
	// the same fold every leaf runs; its ledger drives applyAcceptance. A trivial
	// composite accepts immediately on the all-children-accepted rollup.
	if !contract.IsTrivial() {
		return s.applyAcceptance(ctx, id)
	}

	return s.withTx(ctx, func(q *sqlc.Queries) error {
		cur, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if cur.Kind != KindComposite || cur.Lifecycle != LifecycleActive {
			return ErrInvalidTransition
		}
		// The composite's accepted output is the synthesized fact that all its
		// required children accepted; downstream consumers read its summary.
		accepted := AcceptedOutput{
			GoalID:     cur.ID,
			Summary:    cur.Title,
			AcceptedAt: s.now(),
		}
		rows, err := q.AcceptGoal(ctx, sqlc.AcceptGoalParams{
			AcceptedOutput: marshalNullJSON(accepted),
			ID:             cur.ID,
		})
		if err != nil {
			return fmt.Errorf("rollup accept: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not active (raced)
		}
		return s.bumpParentCounter(ctx, q, cur, counterAccepted)
	})
}

// RollupFail moves a composite to rejected_final because a required child
// reached a terminal-bad state (contract §6, RollupComposite ⇒ fail), bumping
// this parent's own parent required_failed counter. One tx.
func (s *GoalService) RollupFail(ctx context.Context, id string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Kind != KindComposite || d.Lifecycle != LifecycleActive {
			return ErrInvalidTransition
		}
		if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
			return fmt.Errorf("clear active attempt: %w", err)
		}
		rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
			ToLifecycle:   LifecycleRejectedFinal,
			BlockReason:   "",
			ID:            d.ID,
			FromLifecycle: LifecycleActive,
		})
		if err != nil {
			return fmt.Errorf("rollup fail: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return s.bumpParentCounter(ctx, q, d, counterFailed)
	})
}
