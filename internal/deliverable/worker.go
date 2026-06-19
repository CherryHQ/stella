package deliverable

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

// heartbeatInterval is how often the worker extends lease_expires_at on the
// running attempt. Set to 0 to disable heartbeats (tests).
const heartbeatInterval = 20 * time.Second

// leaseDuration is the lease applied while an attempt is in flight. Must be
// > 3 * heartbeatInterval so a single missed beat does not expire the lease.
const leaseDuration = 90 * time.Second

// Worker runs ONE claimed execution/decomposition attempt to its single durable
// transition. It is the only place the executor (agent IO) and the CheckRunner
// (sandbox IO) run; both are pure with respect to durable state. The worker
// applies EXACTLY ONE service transition at the boundary (contract §5 steps
// 4–7): submit→fold on a submitted attempt, or a finalize-failed on a failed
// one. Nothing the worker does writes acceptance_state/counters — only the
// service, through Submit→applyAcceptance, does.
type Worker struct {
	svc       *DeliverableService
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
func NewWorker(svc *DeliverableService, q *sqlc.Queries) *Worker {
	return &Worker{
		svc:       svc,
		q:         q,
		exec:      svc.exec,
		checks:    svc.checks,
		heartbeat: heartbeatInterval,
		lease:     leaseDuration,
		log:       slog.Default().With("component", "deliverable/worker"),
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
func (w *Worker) Run(ctx context.Context, deliverableID, attemptID string, actor Actor) (err error) {
	att, err := w.q.GetAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("worker: load attempt: %w", err)
	}
	dlv, err := w.q.GetDeliverable(ctx, deliverableID)
	if err != nil {
		return fmt.Errorf("worker: load deliverable: %w", err)
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
	// terminal transition against a retry that re-claimed the deliverable.
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
			w.log.Error("worker executor panicked", "deliverable_id", deliverableID, "attempt_id", attemptID, "panic", r)
			w.failAttempt(deliverableID, attemptID, fmt.Sprintf("executor panic: %v", r))
			err = fmt.Errorf("executor panic: %v", r)
		}
	}()

	res, eerr := w.exec.Execute(ctx, ExecutorRequest{
		Deliverable: dlv,
		Attempt:     att,
		Input:       w.attemptInput(att),
	})
	if eerr != nil {
		// The executor encodes outcomes in its Result; a returned error is
		// unexpected. Record it as a failed attempt so convergence can recover.
		w.log.Warn("worker: executor returned error", "deliverable_id", deliverableID, "attempt_id", attemptID, "err", eerr)
		res = ExecutorResult{Failed: true, FailReason: fmt.Sprintf("executor error: %v", eerr), Retryable: true}
	}
	return w.applyResult(deliverableID, dlv, att, actor, res)
}

// applyResult maps the executor's Result to the SINGLE durable transition. A
// fresh context is used so the outcome is recorded even if the dispatch context
// was cancelled (e.g. on shutdown).
func (w *Worker) applyResult(deliverableID string, dlv sqlc.AgentDlvDeliverable, att sqlc.AgentDlvAttempt, actor Actor, res ExecutorResult) error {
	ctx := context.Background()

	switch {
	case res.Submitted:
		// Run deterministic checks (sandbox IO, no SQLite writer held), append
		// each as an acceptance_event, then apply the one submit transition.
		// Submit folds the now-complete ledger via applyAcceptance.
		w.runChecks(ctx, dlv, att, res.Output)
		err := w.svc.Submit(ctx, att.ID, res.Evidence, res.Output)
		if errors.Is(err, ErrInvalidEvidence) {
			// An empty handoff on a non-root deliverable is a protocol miss, not
			// a deliverable failure: finalize this attempt as a retryable failure
			// so convergence re-mints with the same budget.
			reason := "submitted without a handoff summary"
			w.failAttempt(deliverableID, att.ID, reason)
			return nil
		}
		return err

	case res.Failed:
		// A reported executor failure. There is no executor-driven deliverable
		// "block" in this model — block(needs_verdict) is derived by the fold,
		// block(dep) by the dispatcher. The single transition here is to finalize
		// the attempt as failed and release the deliverable; convergence then
		// mints the next attempt (budget left) or blocks/abandons (budget out).
		reason := res.FailReason
		if reason == "" {
			reason = "unspecified executor failure"
		}
		w.failAttempt(deliverableID, att.ID, reason)
		return nil

	default:
		// The executor ran but reported neither submit nor fail (a protocol miss
		// / silent exit). Treat as a failed attempt so the deliverable never
		// strands with a live attempt that produced nothing.
		w.log.Warn("worker: executor produced no action", "deliverable_id", deliverableID, "attempt_id", att.ID)
		w.failAttempt(deliverableID, att.ID, "agent exited without submitting or failing")
		return nil
	}
}

// runChecks runs every required deterministic contract item through the
// CheckRunner and appends each result as an acceptance_event in its own service
// tx, BEFORE the submit fold reads the ledger. Sandbox IO must never hold the
// SQLite writer, so each check runs outside any tx and only its result row is
// written transactionally. A nil runner or a non-deterministic contract is a
// no-op (judgment items are routed by the fold, not run here).
func (w *Worker) runChecks(ctx context.Context, dlv sqlc.AgentDlvDeliverable, att sqlc.AgentDlvAttempt, out AttemptOutput) {
	if w.checks == nil {
		return
	}
	var contract AcceptanceContract
	if err := unmarshalJSON(dlv.AcceptanceContract, &contract); err != nil {
		w.log.Warn("worker: unmarshal contract for checks failed", "deliverable_id", dlv.ID, "err", err)
		return
	}
	env := w.checkEnv(ctx, dlv)
	for _, item := range contract.Items {
		if item.Kind != ItemDeterministic || !item.Required {
			continue
		}
		cr, err := w.checks.Run(ctx, item, env)
		if err != nil {
			// A runner error leaves the item with no event → the fold reads it as
			// pending and the deliverable waits rather than passing on a phantom
			// check. Record nothing; log for tracing.
			w.log.Warn("worker: deterministic check run failed", "deliverable_id", dlv.ID, "item_id", item.ID, "err", err)
			continue
		}
		w.appendCheckEvent(ctx, dlv.ID, att.ID, item, cr)
	}
}

// appendCheckEvent writes one deterministic acceptance_event in a single
// service tx (the service owns every durable write). The event carries the
// item id/command, exit code, cache_key, system authority, and a truncated
// stdout in detail. Best-effort: a write failure leaves the item pending so the
// fold waits rather than mis-deciding.
func (w *Worker) appendCheckEvent(ctx context.Context, deliverableID, attemptID string, item AcceptanceItem, cr CheckResult) {
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
			DeliverableID: deliverableID,
			AttemptID:     nullStr(attemptID),
			ItemID:        item.ID,
			ItemKind:      ItemDeterministic,
			Result:        result,
			Command:       item.Command,
			ExitCode:      sql.NullInt64{Int64: int64(cr.ExitCode), Valid: true},
			CacheKey:      cr.CacheKey,
			Authority:     AuthoritySystem,
			Detail:        detail,
		})
		return e
	})
	if err != nil {
		w.log.Warn("worker: append check acceptance_event failed", "deliverable_id", deliverableID, "item_id", item.ID, "err", err)
	}
}

// checkEnv assembles the provenance the cache key folds. RepoTreeHash/EnvHash
// stay "" because stella's sandbox does not yet guarantee a stable repo/env
// hash — that forces a cache miss (a re-run is cheap; a false hit ships broken
// work, contract §4.1). UpstreamHashes are the accepted-output hashes of this
// deliverable's upstream edges.
func (w *Worker) checkEnv(ctx context.Context, dlv sqlc.AgentDlvDeliverable) CheckEnv {
	env := CheckEnv{DeliverableID: dlv.ID}
	edges, err := w.q.ListEdgeWithUpstreamState(ctx, dlv.ID)
	if err != nil {
		w.log.Warn("worker: list upstream edges for check env failed", "deliverable_id", dlv.ID, "err", err)
		return env
	}
	for _, e := range edges {
		if !e.UpstreamOutput.Valid {
			continue
		}
		var ao AcceptedOutput
		if err := unmarshalJSON(e.UpstreamOutput.String, &ao); err != nil || ao.Hash == "" {
			continue
		}
		env.UpstreamHashes = append(env.UpstreamHashes, ao.Hash)
	}
	return env
}

// attemptInput decodes the attempt's frozen input_context. A decode failure
// degrades to the deliverable intent so the executor still has a prompt rather
// than failing the attempt on a malformed column.
func (w *Worker) attemptInput(att sqlc.AgentDlvAttempt) AttemptInput {
	var in AttemptInput
	if err := unmarshalJSON(att.InputContext, &in); err != nil {
		w.log.Warn("worker: decode attempt input_context failed", "attempt_id", att.ID, "err", err)
	}
	in.AttemptNo = int(att.AttemptNo)
	return in
}

// failAttempt records the single failed-attempt transition: finalize the
// attempt row failed and clear the deliverable's active_attempt_id so the
// dispatcher's convergence tick can mint the next attempt (or block/abandon on
// budget). Best-effort with a fresh context so a cancelled dispatch still
// records the outcome. It writes no acceptance_state/counters — convergence
// owns the lifecycle decision.
func (w *Worker) failAttempt(deliverableID, attemptID, reason string) {
	ctx := context.Background()
	err := w.svc.withTx(ctx, func(qtx *sqlc.Queries) error {
		if _, e := qtx.FinalizeAttempt(ctx, sqlc.FinalizeAttemptParams{
			ToStatus: AttemptFailed,
			Error:    reason,
			ID:       attemptID,
		}); e != nil {
			return e
		}
		return qtx.ClearDeliverableActiveAttempt(ctx, deliverableID)
	})
	if err != nil {
		w.log.Warn("worker: finalize failed attempt failed", "deliverable_id", deliverableID, "attempt_id", attemptID, "err", err)
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
				// A missed beat (e.g. SQLITE_BUSY) silently shortens the lease;
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

// leaseUntil returns the next lease expiry stamp in the naive-UTC TEXT format
// the columns use, anchored to the service clock so tests can drive it.
func (w *Worker) leaseUntil() sql.NullString {
	return sql.NullString{
		String: w.svc.clock().Add(w.lease).UTC().Format(time.RFC3339Nano),
		Valid:  true,
	}
}
