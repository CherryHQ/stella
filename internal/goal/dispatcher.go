package goal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

// DispatcherConfig wires a Dispatcher. Zero-valued fields fall back to the
// package defaults below.
type DispatcherConfig struct {
	Service *GoalService
	Queries *sqlc.Queries
	// Enqueuer enqueues one durable River job per claimed attempt (River Phase
	// 2a). A nil enqueuer disables dispatch (tests that drive Worker.Run directly).
	Enqueuer goalEnqueuer

	TickEvery  time.Duration // 0 ⇒ defaultTickEvery
	LeaseTTL   time.Duration // 0 ⇒ service LeaseTTL
	BatchLimit int           // 0 ⇒ defaultBatchLimit
	// MaxConcurrentPerUser caps in-flight attempts per user (§5/§10.8, default
	// 16). 0 ⇒ the service's configured per-user cap.
	MaxConcurrentPerUser int
	Logger               *slog.Logger
}

const (
	defaultTickEvery  = 2 * time.Second
	defaultBatchLimit = 50
)

// Dispatcher drives the convergence loop on a tick: reap stale attempts,
// propagate dep failures, roll up composites, then scan-and-claim dispatchable
// leaves under the concurrency caps and enqueue a durable River job per claim
// (contract §5, §7; River Phase 2a). It is the only scheduler; every durable
// write still routes through GoalService, and every attempt now executes as a
// River job rather than an in-process goroutine.
type Dispatcher struct {
	cfg DispatcherConfig

	mu      sync.Mutex
	stopped bool
	// zombieLogged dedupes the liveness-backstop warning to once per goal per
	// process, so a persistent zombie is visible without flooding every tick.
	zombieLogged map[string]bool
}

// NewDispatcher constructs a dispatcher, filling defaults for zero-valued
// config fields.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.TickEvery == 0 {
		cfg.TickEvery = defaultTickEvery
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
	return &Dispatcher{cfg: cfg, zombieLogged: map[string]bool{}}
}

// SetEnqueuer injects the River enqueuer the dispatcher uses to dispatch claimed
// attempts. Called by the composition root after the shared client is built and
// before the tick starts; a nil enqueuer leaves dispatch disabled.
func (d *Dispatcher) SetEnqueuer(e goalEnqueuer) {
	d.cfg.Enqueuer = e
}

// TickInterval is the convergence-tick period. The composition root reads it to
// register the single-leader River periodic tick (River Phase 2b).
func (d *Dispatcher) TickInterval() time.Duration { return d.cfg.TickEvery }

// Stop signals the tick to go quiet. In-flight attempts now run as River jobs,
// and the tick itself runs as a River job; draining both is the shared River
// client's responsibility, not the dispatcher's.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
}

// Tick runs one pass of the dispatch loop. Public so tests can drive it
// deterministically. The order is fixed: reap -> rollup -> decompose -> review ->
// claim -> zombie backstop.
func (d *Dispatcher) Tick(ctx context.Context) {
	if d.isStopped() {
		return
	}
	now := d.cfg.Service.clock().UTC()
	d.reapStaleAttempts(ctx, now)
	d.rollupComposites(ctx, now)
	d.scanAndDecompose(ctx, now)
	d.scanAndReview(ctx, now)
	d.scanAndClaim(ctx, now)
	d.detectZombies(ctx)
}

// detectZombies is the liveness backstop (warn-only): it surfaces non-terminal
// goals parked in a state nothing drives (see ListZombieGoals). The transition
// table prevents writing such states; this catches whatever slips past it —
// legacy rows, out-of-band writes, or an invariant we have not met yet. It
// deliberately repairs nothing: each zombie class has its own correct recovery,
// and auto-guessing here would hide the bug that produced it.
func (d *Dispatcher) detectZombies(ctx context.Context) {
	rows, err := d.cfg.Queries.ListZombieGoals(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list zombie goals", "err", err)
		return
	}
	for _, g := range rows {
		d.mu.Lock()
		seen := d.zombieLogged[g.ID]
		d.zombieLogged[g.ID] = true
		d.mu.Unlock()
		if seen {
			continue
		}
		d.cfg.Logger.Warn("dispatcher: zombie goal — non-terminal state with no driver",
			"goal", g.ID, "kind", g.Kind, "lifecycle", g.Lifecycle,
			"block_reason", g.BlockReason, "updated_at", g.UpdatedAt)
	}
}

func (d *Dispatcher) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// reapStaleAttempts finds queued/running attempts whose lease has expired and
// finalizes them as 'interrupted' through the service, which returns the goal to
// ready within its convergence budget (contract §2.2). The lease is authoritative
// across nodes: a live attempt's River worker heartbeats it forward, so an
// expired lease means a genuine orphan.
func (d *Dispatcher) reapStaleAttempts(ctx context.Context, now time.Time) {
	stale, err := d.cfg.Queries.ListStaleAttempts(ctx, sqlc.ListStaleAttemptsParams{
		Now:   pgtype.Timestamptz{Time: now, Valid: true},
		Limit: int32(d.cfg.BatchLimit),
	})
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list stale attempts", "err", err)
		return
	}
	for _, a := range stale {
		// The lease is the single, multi-node liveness signal: a live attempt's
		// River worker heartbeats it forward, so a lease in the past means the job
		// genuinely orphaned (never picked up within the claim grace, or its node
		// crashed). No in-process guard — that was the old single-process design.
		if err := d.cfg.Service.ReapAttempt(ctx, a.ID); err != nil && !errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: reap attempt", "attempt", a.ID, "goal", a.GoalID, "err", err)
		}
	}
}

// rollupComposites drives parent acceptance from a derived required-child tally.
// Stored rollup counters and dep-block propagation are intentionally gone.
func (d *Dispatcher) rollupComposites(ctx context.Context, _ time.Time) {
	parents, err := d.cfg.Queries.ListRollupCandidates(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list rollup candidates", "err", err)
		return
	}
	for _, parent := range parents {
		d.applyRollup(ctx, parent)
	}
}

func (d *Dispatcher) applyRollup(ctx context.Context, parent sqlc.AgentGoal) {
	tally, err := d.cfg.Queries.GetRequiredChildRollupCounts(ctx, parent.ID)
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: tally rollup", "goal", parent.ID, "err", err)
		return
	}
	switch RollupComposite(parent, tally) {
	case RollupAcceptParent:
		if err := d.cfg.Service.RollupAccept(ctx, parent.ID); err != nil &&
			!errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: rollup accept", "goal", parent.ID, "err", err)
		}
	case RollupBlock:
		return
	case RollupFail:
		if err := d.cfg.Service.RollupFail(ctx, parent.ID); err != nil &&
			!errors.Is(err, ErrInvalidTransition) {
			d.cfg.Logger.Warn("dispatcher: rollup fail", "goal", parent.ID, "err", err)
		}
	case RollupWait:
		return
	}
}

// scanAndClaim picks dispatchable leaves, enforces readiness + the per-root and
// per-user concurrency caps (§5/§10.8), resolves the executor, claims through
// the service, and enqueues a durable River job per claim (River Phase 2a).
// Execution concurrency is bounded by the goal queue's MaxWorkers plus the
// per-root/per-user caps enforced here, so no in-process worker-pool gate is
// needed. A nil enqueuer (test wiring) skips dispatch.
func (d *Dispatcher) scanAndClaim(ctx context.Context, now time.Time) {
	if d.cfg.Enqueuer == nil {
		return
	}
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

		// Claim mints the attempt AND enqueues its durable job in one tx (River
		// Phase 2c): on success both committed; on enqueue failure the whole claim
		// rolls back, leaving the goal ready to re-claim next tick — no orphaned
		// claim to reap.
		if _, err := d.cfg.Service.Claim(ctx, goal.ID, execID, d.enqueueAttemptTx); err != nil {
			if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrConcurrencyCap) {
				continue // lost the race or the service-side cap fired
			}
			d.cfg.Logger.Warn("dispatcher: claim", "goal", goal.ID, "err", err)
			continue
		}
		rootInflight[goal.RootID]++
		userInflight[goal.UserID]++
	}
}

// scanAndDecompose drives autonomous planning: every composite awaiting
// decomposition (draft, not yet planned) gets
// a headless decomposition attempt minted + enqueued in one tx, moving it
// draft→active so the next tick skips it. The River worker runs the planner and
// applies the result (SubmitDecomposition) or recovers it on failure. A nil
// enqueuer (test wiring) skips dispatch — the same gate scanAndClaim uses.
func (d *Dispatcher) scanAndDecompose(ctx context.Context, _ time.Time) {
	if d.cfg.Enqueuer == nil {
		return
	}
	candidates, err := d.cfg.Queries.ListDecomposableComposites(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list decomposable composites", "err", err)
		return
	}
	for _, goal := range candidates {
		if d.isStopped() {
			return
		}
		// BeginAutoDecomposition re-checks eligibility under the row lock, so a lost
		// race (manual plan staged since the scan) returns ErrInvalidTransition.
		if _, err := d.cfg.Service.BeginAutoDecomposition(ctx, goal.ID, d.enqueueAttemptTx); err != nil {
			if errors.Is(err, ErrInvalidTransition) {
				continue
			}
			d.cfg.Logger.Warn("dispatcher: begin auto decomposition", "goal", goal.ID, "err", err)
		}
	}
}

// scanAndReview drives agent auto-review: every goal parked blocked(needs_verdict)
// with a pending authority=agent item gets a headless purpose=review attempt
// minted + enqueued in one tx, leaving the goal blocked until the verdict folds
// in (contract §10.13). BeginReview re-checks eligibility under the row lock and
// returns ErrInvalidTransition for goals awaiting a human, with no pending agent
// item, or out of review budget — all skipped here. A nil enqueuer (test wiring)
// skips dispatch, the same gate scanAndClaim/scanAndDecompose use.
//
// Review attempts are LLM jobs like execution attempts, so they share the per-root
// and per-user concurrency caps (§5/§10.8): a user with many blocked goals can no
// longer burst a review job for each one past their execution budget. A goal over
// the cap stays blocked(needs_verdict) and is retried next tick once a slot frees.
func (d *Dispatcher) scanAndReview(ctx context.Context, _ time.Time) {
	if d.cfg.Enqueuer == nil {
		return
	}
	candidates, err := d.cfg.Queries.ListGoalsBlockedNeedsVerdict(ctx, int32(d.cfg.BatchLimit))
	if err != nil {
		d.cfg.Logger.Warn("dispatcher: list needs-verdict goals", "err", err)
		return
	}
	// Per-tick concurrency snapshot shared across the candidates, mirroring
	// scanAndClaim: a minted review raises the in-flight count, so cache counts per
	// root/user and bump locally to honor the cap across a burst within one tick.
	rootInflight := map[string]int64{}
	userInflight := map[string]int64{}

	for _, goal := range candidates {
		if d.isStopped() {
			return
		}
		ok, err := d.underConcurrencyCap(ctx, goal, rootInflight, userInflight)
		if err != nil {
			d.cfg.Logger.Warn("dispatcher: review concurrency count", "goal", goal.ID, "err", err)
			continue
		}
		if !ok {
			continue // over the per-root or per-user budget; retry next tick
		}
		if _, err := d.cfg.Service.BeginReview(ctx, goal.ID, d.enqueueAttemptTx); err != nil {
			if errors.Is(err, ErrInvalidTransition) {
				continue // nothing to agent-review (human-only, budget spent, or raced)
			}
			d.cfg.Logger.Warn("dispatcher: begin review", "goal", goal.ID, "err", err)
			continue
		}
		rootInflight[goal.RootID]++
		userInflight[goal.UserID]++
	}
}

// enqueueAttemptTx inserts the durable execution job for a claimed attempt inside
// the claim's transaction (River Phase 2c). Passed to GoalService.Claim as the
// AttemptEnqueuer so claim+enqueue are atomic; an error here aborts the claim.
func (d *Dispatcher) enqueueAttemptTx(ctx context.Context, tx pgx.Tx, goalID, attemptID string) error {
	res, err := d.cfg.Enqueuer.InsertTx(ctx, tx, goalAttemptArgs{GoalID: goalID, AttemptID: attemptID}, goalInsertOpts())
	if err != nil {
		return err
	}
	// attempt_id is freshly minted in this same tx, so the unique key can never
	// already exist: a skipped-as-duplicate result means a real invariant breach
	// (a stale/duplicate attempt id). Fail the enqueue so the claim rolls back
	// rather than committing an attempt whose job points at a different insert.
	if res.UniqueSkippedAsDuplicate {
		return fmt.Errorf("goal: attempt job skipped as duplicate for a freshly minted attempt %s", attemptID)
	}
	return nil
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
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// Finalize the stale attempt as interrupted. A 0-row update means it is no
		// longer queued/running (already finalized/raced) — idempotent no-op the
		// dispatcher ignores as ErrInvalidTransition.
		rows, err := s.finalizeAttempt(ctx, q, att, AttemptInterrupted, "lease expired; reaped by dispatcher", FailureClassFlaky)
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
		// The goal may have reached a terminal state while this attempt was still
		// leased: Cancel only finalizes the attempt named by active_attempt_id, and
		// decomposition/review attempts are never pointed there. The attempt is
		// finalized above; routing recovery here would resurrect a terminal
		// lifecycle (a cancelled composite came back as ready this way). Stop.
		if IsTerminalLifecycle(d.Lifecycle) {
			return nil
		}
		// A decomposition attempt's goal is 'active' running the plan; on reap release
		// it back to draft so it is re-decomposed within budget (or block when spent).
		// A still-'queued' reap was never picked up by River (queue backpressure), so
		// it does not charge the plan budget — only a 'running' reap that genuinely
		// executed does. att.Status is the pre-finalize status (GetAttempt ran before
		// FinalizeAttempt) — do not reorder without revisiting this.
		if att.Purpose == PurposeDecomposition {
			if att.Status == AttemptQueued {
				return s.recoverDecomposition(ctx, q, d, false)
			}
			return s.routeFailedAttempt(ctx, q, d, attemptID, "lease expired; reaped by dispatcher", FailureClassFlaky, true)
		}
		// A reaped review attempt leaves the goal blocked(needs_verdict); the
		// dispatcher re-mints within the per-episode review budget, then degrades to
		// a human. A running reap (started_at set) charges one budget unit; a queued
		// reap that never ran does not (CountRanReviewAttemptsForOutput filters on
		// started_at), mirroring the queued-decomposition refund below.
		if att.Purpose == PurposeReview {
			return nil
		}

		// A still-'queued' reap never executed: its River job sat behind the queue's
		// MaxWorkers and the claim-grace lease expired before any PromoteAttempt (queue
		// backpressure under wide fanout). ClaimGoal already charged one budget unit at
		// claim time, so charging it here too would burn budget on an attempt that never
		// ran and park the goal blocked(budget_exhausted) without a single execution.
		// Refund and reopen instead. A 'running' reap genuinely executed (or its node
		// crashed mid-run), so it still consumes budget as before. att.Status is the
		// pre-finalize status (GetAttempt above runs before FinalizeAttempt) — do not
		// reorder those without revisiting this branch.
		if att.Status == AttemptQueued {
			return s.reopenForRework(ctx, q, d)
		}
		// Execution attempt: a lease expiry is infrastructure-flaky. It reopens on
		// the flaky counter without charging the model business budget.
		return s.routeFailedAttempt(ctx, q, d, attemptID, "lease expired; reaped by dispatcher", FailureClassFlaky, false)
	})
}

// childAcceptedOutputs returns the frozen accepted output of every accepted
// child of parentID, in plan order. It feeds a composite's rollup output so the
// parent carries its deliverables inline. A child with no (or malformed)
// accepted output is skipped rather than failing the rollup — a missing
// snapshot must never block the parent's acceptance.
func childAcceptedOutputs(ctx context.Context, q *sqlc.Queries, parentID string) ([]AcceptedOutput, error) {
	rows, err := q.ListGoalChildren(ctx, pgtype.Text{String: parentID, Valid: true})
	if err != nil {
		return nil, err
	}
	var out []AcceptedOutput
	for _, c := range rows {
		if !c.AcceptedOutput.Valid {
			continue
		}
		var ao AcceptedOutput
		if err := unmarshalNullJSON(c.AcceptedOutput, &ao); err != nil {
			continue
		}
		out = append(out, ao)
	}
	return out, nil
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
		// required children accepted; it also carries each accepted child's frozen
		// output so a reader of the parent sees the deliverables without walking
		// children (a composite produces no work of its own).
		kids, err := childAcceptedOutputs(ctx, q, cur.ID)
		if err != nil {
			return fmt.Errorf("collect child outputs: %w", err)
		}
		accepted := AcceptedOutput{
			GoalID:     cur.ID,
			Summary:    cur.Title,
			AcceptedAt: s.now(),
			Children:   kids,
		}
		rows, err := s.acceptGoal(ctx, q, cur, accepted)
		if err != nil {
			return fmt.Errorf("rollup accept: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not active (raced)
		}
		return nil
	})
}

// RollupFail moves a composite to done(failed) because a required child reached
// a terminal-bad state (contract §6, RollupComposite => fail). One tx.
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
		rows, err := s.transitionGoalLifecycle(ctx, q, d, LifecycleDone, "")
		if err != nil {
			return fmt.Errorf("rollup fail: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return nil
	})
}
