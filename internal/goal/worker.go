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
	"github.com/CherryHQ/stella/pkg/sandbox"
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
//   - run the executor and, for a submitted attempt, run the deterministic
//     CheckRunner from the terminal turn's live sandbox close callback, append
//     acceptance_events, and apply Submit → applyAcceptance;
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

	if err := w.svc.promoteAttempt(ctx, attemptID, w.leaseUntil()); err != nil {
		if errors.Is(err, ErrInvalidTransition) {
			return ErrInvalidTransition
		}
		return fmt.Errorf("worker: promote attempt: %w", err)
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
			_ = w.failAttempt(context.WithoutCancel(ctx), goalID, attemptID, fmt.Sprintf("executor panic: %v", r), FailureClassEnvironment, BlockEnvUnavailable)
			err = fmt.Errorf("executor panic: %v", r)
		}
	}()

	var checkErr error
	checksRan := false
	res, eerr := w.exec.Execute(ctx, ExecutorRequest{
		Goal:    goal,
		Attempt: att,
		Input:   w.attemptInput(att),
		OnSandboxSession: func(sess sandbox.Session) error {
			checksRan = true
			checkErr = w.runChecks(ctx, goal, att, sess)
			return nil
		},
	})
	if eerr != nil {
		if errors.Is(eerr, context.Canceled) || errors.Is(eerr, context.DeadlineExceeded) {
			return eerr
		}
		// The executor encodes pre-execution failures in its Result. A returned
		// error is therefore outcome-unknown: model, tool, filesystem, or outbound
		// work may have happened before the transport/runtime error surfaced.
		// Block for explicit recovery rather than transparently minting another
		// attempt that could replay those effects.
		w.log.Warn("worker: executor returned error", "goal_id", goalID, "attempt_id", attemptID, "err", eerr)
		res = ExecutorResult{Failed: true, FailReason: fmt.Sprintf("executor outcome unknown: %v", eerr), FailureClass: FailureClassEnvironment, BlockedBy: BlockEnvUnavailable}
	}
	applyCtx := context.WithoutCancel(ctx)
	if res.completion != nil {
		defer res.completion.Release()
		applyCtx, err = res.completion.Context(applyCtx)
		if err != nil {
			return fmt.Errorf("worker: bind AgentRun completion fence: %w", err)
		}
	}
	return w.applyResult(applyCtx, goalID, goal, att, actor, res, checksRan, checkErr)
}

// applyResult maps the executor's Result to the SINGLE durable transition. A
// fresh context is used so the outcome is recorded even if the dispatch context
// was cancelled (e.g. on shutdown).
func (w *Worker) applyResult(ctx context.Context, goalID string, goal sqlc.AgentGoal, att sqlc.AgentGoalAttempt, actor Actor, res ExecutorResult, checksRan bool, checkErr error) error {
	switch {
	case att.Purpose == PurposeDecomposition:
		// A planner attempt has a different outcome shape (a plan, not an output)
		// and never runs deterministic checks, so it is handled wholly apart from
		// the execution/review fold. Routed by purpose BEFORE the generic submit
		// case so a decomposition never falls through to the leaf checks.
		return w.applyDecompositionResult(ctx, goal, att, res)

	case att.Purpose == PurposeReview:
		// A reviewer attempt's outcome is verdicts, not an output, and runs no
		// deterministic checks. Routed by purpose BEFORE the generic submit case so
		// it never falls through to the leaf checks/Submit.
		return w.applyReviewResult(ctx, goalID, att, res)

	case res.Submitted:
		// Deterministic checks ran in the terminal turn's pre-close sandbox callback
		// so they used the exact live sandbox the agent just modified. Submit folds
		// the now-complete ledger via applyAcceptance.
		if w.hasRequiredDeterministic(goal) && !checksRan {
			checkErr = ErrNoSandbox
		}
		if cerr := checkErr; cerr != nil {
			// A required deterministic gate could not be evaluated (no runner, a
			// sandbox error, or a failed append). Submitting now would strand the
			// goal 'active' on a pending item with no future event source
			// (issue #543); fail the attempt so convergence retries within budget
			// and ultimately blocks for a human if it persists.
			return w.failAttempt(ctx, goalID, att.ID, "deterministic check could not be evaluated: "+cerr.Error(), FailureClassEnvironment, BlockEnvUnavailable)
		}
		err := w.svc.Submit(ctx, att.ID, res.Evidence, res.Output)
		if errors.Is(err, ErrInvalidEvidence) {
			// An empty handoff on a non-root goal is a protocol miss, not
			// a goal failure: finalize this attempt as a retryable failure
			// so convergence re-mints with the same budget.
			reason := "submitted without a handoff summary"
			return w.failAttempt(ctx, goalID, att.ID, reason, FailureClassModel)
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
		return w.failAttempt(ctx, goalID, att.ID, reason, failureClassForResult(res), res.BlockedBy)

	default:
		// The executor ran but reported neither submit nor fail (a protocol miss
		// / silent exit). Treat as a failed attempt so the goal never
		// strands with a live attempt that produced nothing.
		w.log.Warn("worker: executor produced no action", "goal_id", goalID, "attempt_id", att.ID)
		return w.failAttempt(ctx, goalID, att.ID, "agent exited without submitting or failing", FailureClassModel)
	}
}

// applyDecompositionResult applies a planner attempt's outcome as the single
// durable transition. Structural plan errors are fed back to the same planning
// session for a bounded repair loop; model failures use the planning budget,
// while flaky/environment/contract failures route by responsibility.
func (w *Worker) applyDecompositionResult(ctx context.Context, goal sqlc.AgentGoal, att sqlc.AgentGoalAttempt, res ExecutorResult) error {
	defer func() {
		if res.completion != nil {
			res.completion.Release()
		}
	}()
	input := w.attemptInput(att)
	repairMax := plannerRepairMax(goal)
	for repairs := 0; ; {
		if res.Submitted && res.Decomposition != nil {
			w.recordRepairRounds(ctx, goal.ID, att.ID, repairs)
			if derr := w.svc.SubmitDecomposition(ctx, att.ID, res.Evidence, *res.Decomposition); derr != nil {
				errs := decompositionSubmitErrors(goal, input.MaxDepth, *res.Decomposition, derr)
				if len(errs) > 0 {
					if repairs < repairMax {
						repairs++
						input.PriorErrors = errs
						if res.completion != nil {
							res.completion.Release()
						}
						next, eerr := w.exec.Execute(context.Background(), ExecutorRequest{Goal: goal, Attempt: att, Input: input})
						if eerr != nil {
							w.log.Warn("worker: planner repair executor returned error", "goal_id", goal.ID, "attempt_id", att.ID, "err", eerr)
							res = ExecutorResult{Failed: true, FailReason: fmt.Sprintf("executor outcome unknown: %v", eerr), FailureClass: FailureClassEnvironment, BlockedBy: BlockEnvUnavailable}
						} else {
							res = next
							if res.completion != nil {
								ctx, eerr = res.completion.Context(context.Background())
								if eerr != nil {
									return fmt.Errorf("worker: bind planner repair completion fence: %w", eerr)
								}
							}
						}
						continue
					}
					w.recordRepairRounds(ctx, goal.ID, att.ID, repairs)
					return w.failAttempt(ctx, goal.ID, att.ID, "planning invalid:\n"+RenderErrorsText(errs), FailureClassModel)
				}
				w.recordRepairRounds(ctx, goal.ID, att.ID, repairs)
				return w.failAttempt(ctx, goal.ID, att.ID, "apply decomposition: "+derr.Error(), FailureClassFlaky)
			}
			return nil
		}
		reason := res.FailReason
		if reason == "" {
			reason = "decomposition produced no plan"
		}
		w.recordRepairRounds(ctx, goal.ID, att.ID, repairs)
		return w.failAttempt(ctx, goal.ID, att.ID, reason, failureClassForResult(res), res.BlockedBy)
	}
}

func (w *Worker) recordRepairRounds(ctx context.Context, goalID, attemptID string, repairs int) {
	if repairs < 0 {
		repairs = 0
	}
	if err := w.svc.withTx(ctx, func(qtx *sqlc.Queries) error {
		return qtx.SetAttemptRepairRounds(ctx, sqlc.SetAttemptRepairRoundsParams{ID: attemptID, RepairRounds: int32(repairs)})
	}); err != nil {
		w.log.Warn("worker: record repair rounds failed", "goal_id", goalID, "attempt_id", attemptID, "repair_rounds", repairs, "err", err)
	}
}

func plannerRepairMax(goal sqlc.AgentGoal) int {
	var pol ConvergencePolicy
	_ = unmarshalJSON(goal.ConvergencePolicy, &pol)
	return pol.Normalized().PlannerRepairMax
}

func decompositionSubmitErrors(goal sqlc.AgentGoal, maxDepth int, content DecompositionContent, err error) []ValidationError {
	if len(structuralValidationErrors(err)) == 0 {
		return nil
	}
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	if errs := validateDecompositionDetailed(content, int(goal.Depth), maxDepth); len(errs) > 0 {
		return errs
	}
	if errors.Is(err, ErrDeterministicChecksUnsupported) {
		return deterministicCapabilityErrors(content)
	}
	return structuralValidationErrors(err)
}

// applyReviewResult applies a reviewer attempt's outcome as the single durable
// transition. A submitted attempt carries verdicts, which SubmitReview folds in
// as authority=agent acceptance_events and re-derives acceptance (accept on pass,
// rework on fail). Any non-verdict terminal (fail / no verdict / protocol miss)
// is a failed review attempt that leaves the goal blocked(needs_verdict); the
// dispatcher re-mints within the review budget, then degrades to a human verdict.
// Errors are recorded as a failed attempt, not returned, so applyResult always
// succeeds. Mirrors applyDecompositionResult.
func (w *Worker) applyReviewResult(ctx context.Context, goalID string, att sqlc.AgentGoalAttempt, res ExecutorResult) error {
	if res.Submitted && len(res.Verdicts) > 0 {
		if rerr := w.svc.SubmitReview(ctx, att.ID, res.Evidence, res.Verdicts); rerr != nil {
			return w.failAttempt(ctx, goalID, att.ID, "apply review: "+rerr.Error(), FailureClassFlaky)
		}
		return nil
	}
	reason := res.FailReason
	if reason == "" {
		reason = "review produced no verdict"
	}
	return w.failAttempt(ctx, goalID, att.ID, reason, failureClassForResult(res), res.BlockedBy)
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
func (w *Worker) runChecks(ctx context.Context, goal sqlc.AgentGoal, att sqlc.AgentGoalAttempt, sess sandbox.Session) error {
	var contract AcceptanceContract
	if err := unmarshalJSON(goal.AcceptanceContract, &contract); err != nil {
		// An unparseable contract has no runnable checks; the fold treats it as
		// trivial. Don't fail the attempt on a malformed column.
		w.log.Warn("worker: unmarshal contract for checks failed", "goal_id", goal.ID, "err", err)
		return nil
	}
	required := requiredDeterministicItems(contract)
	if len(required) == 0 {
		return nil
	}
	if w.checks == nil {
		return fmt.Errorf("no check runner for %d required deterministic item(s)", len(required))
	}
	env := w.checkEnv(ctx, goal)
	for _, item := range required {
		cr, err := w.checks.Run(ctx, item, env, sess)
		if err != nil {
			return fmt.Errorf("run deterministic check %q: %w", item.ID, err)
		}
		if err := w.appendCheckEvent(ctx, goal.ID, att.ID, item, cr); err != nil {
			return fmt.Errorf("record deterministic check %q: %w", item.ID, err)
		}
	}
	return nil
}

func (w *Worker) hasRequiredDeterministic(goal sqlc.AgentGoal) bool {
	var contract AcceptanceContract
	if err := unmarshalJSON(goal.AcceptanceContract, &contract); err != nil {
		return false
	}
	return len(requiredDeterministicItems(contract)) > 0
}

func requiredDeterministicItems(contract AcceptanceContract) []AcceptanceItem {
	var required []AcceptanceItem
	for _, item := range contract.Items {
		if item.Kind == ItemDeterministic && item.Required {
			required = append(required, item)
		}
	}
	return required
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
		att, err := qtx.GetAttempt(ctx, attemptID)
		if err != nil {
			return err
		}
		if att.Status != AttemptRunning {
			return ErrInvalidTransition
		}
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
func (w *Worker) failAttempt(ctx context.Context, goalID, attemptID, reason, failureClass string, blockedBy ...string) error {
	if err := w.svc.FailAttempt(ctx, attemptID, reason, failureClass, blockedBy...); err != nil && !errors.Is(err, ErrInvalidTransition) {
		w.log.Warn("worker: finalize failed attempt failed", "goal_id", goalID, "attempt_id", attemptID, "err", err)
		return err
	}
	return nil
}

func failureClassForResult(res ExecutorResult) string {
	if ValidFailureClass(res.FailureClass) && res.FailureClass != "" {
		return res.FailureClass
	}
	return FailureClassModel
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
