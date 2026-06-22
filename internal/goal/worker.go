package goal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// heartbeatInterval is how often the worker extends lease_expires_at on the
// running attempt. Set to 0 to disable heartbeats (tests).
const heartbeatInterval = 20 * time.Second

// leaseDuration is the lease applied while an attempt is in flight. Must be
// > 3 * heartbeatInterval so a single missed beat does not expire the lease.
const leaseDuration = 90 * time.Second

// claimGraceTTL is the lease a freshly claimed (still queued) attempt carries
// until a River worker picks it up and PromoteAttempt extends it. It must exceed
// the worst-case queue pickup latency so the dispatcher reaper never reclaims a
// job that is legitimately still waiting in the goal queue; the per-root/per-user
// caps keep that backlog small. If pickup never happens (insert gap, crash before
// promote), the reaper reopens the goal once this window expires (mintNextAttempt).
const claimGraceTTL = 2 * time.Minute

// Worker runs ONE claimed execution/decomposition attempt to its single durable
// transition. It is the only place the executor (agent IO) and the CheckRunner
// (sandbox IO) run; both are pure with respect to durable state. The worker
// applies EXACTLY ONE service transition at the boundary (contract §5 steps
// 4–7): submit→fold on a submitted attempt, or a finalize-failed on a failed
// one. Nothing the worker does writes acceptance_state/counters — only the
// service, through Submit→applyAcceptance, does.
type Worker struct {
	svc       *GoalService
	q         *sqlc.Queries
	exec      Executor
	checks    CheckRunner
	heartbeat time.Duration
	lease     time.Duration
	log       *slog.Logger
}

// NewWorker wires a worker against a service. The executor and check runner
// default to the ones registered on the service (WithExecutor/WithCheckRunner)
// so the dispatcher can spawn workers with a single dependency.
func NewWorker(svc *GoalService, q *sqlc.Queries) *Worker {
	return &Worker{
		svc:       svc,
		q:         q,
		exec:      svc.exec,
		checks:    svc.checks,
		heartbeat: heartbeatInterval,
		lease:     leaseDuration,
		log:       slog.Default().With("component", "goal/worker"),
	}
}

// SetHeartbeat overrides the heartbeat interval (for tests).
func (w *Worker) SetHeartbeat(d time.Duration) { w.heartbeat = d }

// SetLease overrides the lease duration (for tests).
func (w *Worker) SetLease(d time.Duration) { w.lease = d }

// Run drives one claimed attempt. Responsibilities:
//   - promote the attempt queued→running with started_at + initial lease
//     (a zero-row promote means the attempt was already interrupted/cancelled
//     out from under us — abort with ErrInvalidTransition so a superseded
//     attempt never executes or applies a transition);
//   - keep lease_expires_at fresh while the executor runs (heartbeat);
//   - run the executor, then for a submitted attempt run the deterministic
//     CheckRunner, append the results as acceptance_events via the service, and
//     apply the single submit transition (service.Submit → applyAcceptance);
//   - for a failed (or no-action) attempt apply the single finalize-failed
//     transition so convergence can mint the next attempt or block;
//   - turn an executor panic into a non-retryable failure.
func (w *Worker) Run(ctx context.Context, goalID, attemptID string, actor Actor) (err error) {
	att, err := w.q.GetAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("worker: load attempt: %w", err)
	}
	goal, err := w.q.GetGoal(ctx, goalID)
	if err != nil {
		return fmt.Errorf("worker: load goal: %w", err)
	}

	promoted, perr := w.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{
		LeaseExpiresAt: w.leaseUntil(),
		ID:             attemptID,
	})
	if perr != nil {
		return fmt.Errorf("worker: promote attempt: %w", perr)
	}
	// PromoteAttempt only matches a still-queued attempt. Zero rows means it was
	// interrupted or cancelled (lease reap, shutdown) before the worker started;
	// abort so a superseded attempt never runs the executor or applies a
	// terminal transition against a retry that re-claimed the goal.
	if promoted == 0 {
		return ErrInvalidTransition
	}

	// Start the heartbeat loop in the background.
	hbCtx, hbCancel := context.WithCancel(ctx)
	var hbWG sync.WaitGroup
	if w.heartbeat > 0 {
		hbWG.Add(1)
		go w.heartbeatLoop(hbCtx, &hbWG, attemptID)
	}

	defer func() {
		hbCancel()
		hbWG.Wait()
		if r := recover(); r != nil {
			w.log.Error("worker executor panicked", "goal_id", goalID, "attempt_id", attemptID, "panic", r)
			w.failAttempt(goalID, attemptID, fmt.Sprintf("executor panic: %v", r))
			err = fmt.Errorf("executor panic: %v", r)
		}
	}()

	res, eerr := w.exec.Execute(ctx, ExecutorRequest{
		Goal:    goal,
		Attempt: att,
		Input:   w.attemptInput(att),
	})
	if eerr != nil {
		// The executor encodes outcomes in its Result; a returned error is
		// unexpected. Record it as a failed attempt so convergence can recover.
		w.log.Warn("worker: executor returned error", "goal_id", goalID, "attempt_id", attemptID, "err", eerr)
		res = ExecutorResult{Failed: true, FailReason: fmt.Sprintf("executor error: %v", eerr), Retryable: true}
	}
	return w.applyResult(goalID, goal, att, actor, res)
}

// applyResult maps the executor's Result to the SINGLE durable transition. A
// fresh context is used so the outcome is recorded even if the dispatch context
// was cancelled (e.g. on shutdown).
func (w *Worker) applyResult(goalID string, goal sqlc.AgentGoal, att sqlc.AgentGoalAttempt, actor Actor, res ExecutorResult) error {
	ctx := context.Background()

	switch {
	case att.Purpose == PurposeDecomposition:
		// Autonomous planning: a successful attempt carries the produced plan; apply
		// it (accepted revision → materialize → release children) as the single
		// durable transition. A non-submit terminal (fail / no decomposition /
		// protocol miss) is a failed plan attempt that convergence recovers within
		// the plan budget (recoverDecomposition). Routed by purpose BEFORE the generic
		// submit case so a decomposition never runs the leaf deterministic checks.
		if res.Submitted && res.Decomposition != nil {
			if derr := w.svc.SubmitDecomposition(ctx, att.ID, res.Evidence, *res.Decomposition); derr != nil {
				w.failAttempt(goalID, att.ID, "apply decomposition: "+derr.Error())
			}
			return nil
		}
		reason := res.FailReason
		if reason == "" {
			reason = "decomposition produced no plan"
		}
		w.failAttempt(goalID, att.ID, reason)
		return nil

	case res.Submitted:
		// Run deterministic checks (sandbox IO, no DB tx held), append
		// each as an acceptance_event, then apply the one submit transition.
		// Submit folds the now-complete ledger via applyAcceptance.
		if cerr := w.runChecks(ctx, goal, att, res.Output); cerr != nil {
			// A required deterministic gate could not be evaluated (no runner, a
			// sandbox error, or a failed append). Submitting now would strand the
			// goal 'active' on a pending item with no future event source
			// (issue #543); fail the attempt so convergence retries within budget
			// and ultimately blocks for a human if it persists.
			w.failAttempt(goalID, att.ID, "deterministic check could not be evaluated: "+cerr.Error())
			return nil //nolint:nilerr // failAttempt records the failure transition; applyResult succeeded
		}
		err := w.svc.Submit(ctx, att.ID, res.Evidence, res.Output)
		if errors.Is(err, ErrInvalidEvidence) {
			// An empty handoff on a non-root goal is a protocol miss, not
			// a goal failure: finalize this attempt as a retryable failure
			// so convergence re-mints with the same budget.
			reason := "submitted without a handoff summary"
			w.failAttempt(goalID, att.ID, reason)
			return nil
		}
		return err

	case res.Failed:
		// A reported executor failure. There is no executor-driven goal
		// "block" in this model — block(needs_verdict) is derived by the fold,
		// block(dep) by the dispatcher. The single transition here is to finalize
		// the attempt as failed and release the goal; convergence then
		// mints the next attempt (budget left) or blocks/abandons (budget out).
		reason := res.FailReason
		if reason == "" {
			reason = "unspecified executor failure"
		}
		w.failAttempt(goalID, att.ID, reason)
		return nil

	default:
		// The executor ran but reported neither submit nor fail (a protocol miss
		// / silent exit). Treat as a failed attempt so the goal never
		// strands with a live attempt that produced nothing.
		w.log.Warn("worker: executor produced no action", "goal_id", goalID, "attempt_id", att.ID)
		w.failAttempt(goalID, att.ID, "agent exited without submitting or failing")
		return nil
	}
}

// runChecks runs every required deterministic contract item through the
// CheckRunner and appends each result as an acceptance_event in its own service
// tx, BEFORE the submit fold reads the ledger. Sandbox IO must never run inside a
// DB tx (it would pin a pooled connection across slow IO), so each check runs
// outside any tx and only its result row is written transactionally.
//
// It returns an error when a REQUIRED deterministic item cannot be evaluated (no
// runner configured, a runner error, or a failed event append). A missing event
// would leave that item pending and strand the goal 'active' forever
// (issue #543), so the caller fails the attempt instead — convergence then
// retries within budget and ultimately blocks for a human. A legitimate check
// FAIL is a recorded event, not an error. A contract with no required
// deterministic item is a no-op (judgment items are routed by the fold).
func (w *Worker) runChecks(ctx context.Context, goal sqlc.AgentGoal, att sqlc.AgentGoalAttempt, out AttemptOutput) error {
	var contract AcceptanceContract
	if err := unmarshalJSON(goal.AcceptanceContract, &contract); err != nil {
		// An unparseable contract has no runnable checks; the fold treats it as
		// trivial. Don't fail the attempt on a malformed column.
		w.log.Warn("worker: unmarshal contract for checks failed", "goal_id", goal.ID, "err", err)
		return nil
	}
	var required []AcceptanceItem
	for _, item := range contract.Items {
		if item.Kind == ItemDeterministic && item.Required {
			required = append(required, item)
		}
	}
	if len(required) == 0 {
		return nil
	}
	if w.checks == nil {
		return fmt.Errorf("no check runner for %d required deterministic item(s)", len(required))
	}
	env := w.checkEnv(ctx, goal)
	for _, item := range required {
		cr, err := w.checks.Run(ctx, item, env)
		if err != nil {
			return fmt.Errorf("run deterministic check %q: %w", item.ID, err)
		}
		if err := w.appendCheckEvent(ctx, goal.ID, att.ID, item, cr); err != nil {
			return fmt.Errorf("record deterministic check %q: %w", item.ID, err)
		}
	}
	return nil
}

// appendCheckEvent writes one deterministic acceptance_event in a single
// service tx (the service owns every durable write). The event carries the
// item id/command, exit code, cache_key, system authority, and a truncated
// stdout in detail. Returns an error on a write failure so runChecks can fail the
// attempt rather than strand the item pending (issue #543). A duplicate event
// (same goal/attempt/item/cache_key) is deduped by appendAcceptanceEvent
// and returns nil.
func (w *Worker) appendCheckEvent(ctx context.Context, goalID, attemptID string, item AcceptanceItem, cr CheckResult) error {
	result := ResultFail
	if cr.Pass {
		result = ResultPass
	}
	detail := marshalJSON(AcceptanceEventDetail{
		Stdout:   w.truncStdout(cr.Stdout),
		CacheHit: cr.CacheHit,
	})
	err := w.svc.withTx(ctx, func(qtx *sqlc.Queries) error {
		_, e := w.svc.appendAcceptanceEvent(ctx, qtx, sqlc.AppendAcceptanceEventParams{
			GoalID:    goalID,
			AttemptID: pgnull.Text(attemptID),
			ItemID:    item.ID,
			ItemKind:  ItemDeterministic,
			Result:    result,
			Command:   item.Command,
			ExitCode:  pgtype.Int8{Int64: int64(cr.ExitCode), Valid: true},
			CacheKey:  cr.CacheKey,
			Authority: AuthoritySystem,
			Detail:    detail,
		})
		return e
	})
	if err != nil {
		w.log.Warn("worker: append check acceptance_event failed", "goal_id", goalID, "item_id", item.ID, "err", err)
	}
	return err
}

// checkEnv assembles the provenance the cache key folds. RepoTreeHash/EnvHash
// stay "" because stella's sandbox does not yet guarantee a stable repo/env
// hash — that forces a cache miss (a re-run is cheap; a false hit ships broken
// work, contract §4.1). UpstreamHashes are the accepted-output hashes of this
// goal's upstream edges.
func (w *Worker) checkEnv(ctx context.Context, goal sqlc.AgentGoal) CheckEnv {
	env := CheckEnv{GoalID: goal.ID}
	edges, err := w.q.ListEdgeWithUpstreamState(ctx, goal.ID)
	if err != nil {
		w.log.Warn("worker: list upstream edges for check env failed", "goal_id", goal.ID, "err", err)
		return env
	}
	for _, e := range edges {
		if !e.UpstreamOutput.Valid {
			continue
		}
		var ao AcceptedOutput
		if err := unmarshalNullJSON(e.UpstreamOutput, &ao); err != nil || ao.Hash == "" {
			continue
		}
		env.UpstreamHashes = append(env.UpstreamHashes, ao.Hash)
	}
	return env
}

// attemptInput decodes the attempt's frozen input_context. A decode failure
// degrades to the goal intent so the executor still has a prompt rather
// than failing the attempt on a malformed column.
func (w *Worker) attemptInput(att sqlc.AgentGoalAttempt) AttemptInput {
	var in AttemptInput
	if err := unmarshalJSON(att.InputContext, &in); err != nil {
		w.log.Warn("worker: decode attempt input_context failed", "attempt_id", att.ID, "err", err)
	}
	in.AttemptNo = int(att.AttemptNo)
	return in
}

// failAttempt records a worker-reported failure through the service convergence
// seam (FailAttempt): finalize the attempt failed AND apply the single lifecycle
// move (reopen-for-rework within budget, else block/abandon/reject), so the
// goal never strands 'active' with no live attempt (issue #543). Uses a
// fresh context so a cancelled dispatch still records the outcome. ErrInvalidTransition
// is benign — the attempt was already reaped/raced and the goal recovered.
func (w *Worker) failAttempt(goalID, attemptID, reason string) {
	ctx := context.Background()
	if err := w.svc.FailAttempt(ctx, attemptID, reason); err != nil && !errors.Is(err, ErrInvalidTransition) {
		w.log.Warn("worker: finalize failed attempt failed", "goal_id", goalID, "attempt_id", attemptID, "err", err)
	}
}

// truncStdout caps captured stdout before it touches event detail. Zero/absent
// limit falls back to a conservative 16 KB.
func (w *Worker) truncStdout(s string) string {
	limit := w.svc.cfg.StdoutLimit
	if limit <= 0 {
		limit = 16 << 10
	}
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

func (w *Worker) heartbeatLoop(ctx context.Context, wg *sync.WaitGroup, attemptID string) {
	defer wg.Done()
	t := time.NewTicker(w.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := w.q.HeartbeatAttempt(ctx, sqlc.HeartbeatAttemptParams{
				LeaseExpiresAt: w.leaseUntil(),
				ID:             attemptID,
			})
			if err != nil {
				// A missed beat (e.g. a transient DB error) silently shortens the lease;
				// log it so a lease expiry can be traced to a failed heartbeat.
				w.log.Warn("worker: heartbeat write failed", "attempt_id", attemptID, "err", err)
				continue
			}
			if n == 0 {
				// The attempt is no longer 'running' — its lease was reaped or it
				// was finalized out from under us. The executor keeps going but its
				// terminal write will lose the race.
				w.log.Warn("worker: heartbeat found no running attempt; lease likely already reaped", "attempt_id", attemptID)
			}
		}
	}
}

// leaseUntil returns the next lease expiry instant for the TIMESTAMPTZ lease
// column, anchored to the service clock so tests can drive it.
func (w *Worker) leaseUntil() pgtype.Timestamptz {
	return pgtype.Timestamptz{
		Time:  w.svc.clock().Add(w.lease).UTC(),
		Valid: true,
	}
}
