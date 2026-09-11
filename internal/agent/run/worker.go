package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/core/agenterr"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Executor runs one claimed run's turn and returns the final reply text. The
// production implementation re-derives authority from the run's serialized
// actor facts and drives agent.Service.Chat under the adopted lease context.
// Run state, reply ops, and the execution lease all finish elsewhere — in the
// lease's finish transaction.
type Executor interface {
	Execute(ctx context.Context, run sqlc.AgentRun) (reply string, err error)
}

// FinishHook runs inside the execution-finish transaction, after the run's
// own terminal transition. The channel side uses it to append the reply's
// outbox operations, so a reply exists before any external send is attempted.
type FinishHook func(ctx context.Context, tx pgx.Tx, run sqlc.AgentRun, result, reply string) error

// Worker claims queued runs and executes them under the session execution
// lease. It is deliberately dumb about routing: the run row already carries
// session, agent, actor and reply address.
type Worker struct {
	db       *pgxpool.Pool
	runs     *Store
	exec     *sessionexecution.Store
	id       string
	executor Executor
	onFinish FinishHook
	poll     time.Duration
	// turnAppender, when bound, writes the run's deferred transcript rows
	// inside the finish transaction (plan D4 single committer).
	turnAppender func(ctx context.Context, tx pgx.Tx, session memory.Session, msgs []ai.Message) error
	// execCtx parents the adopted lease context. It outlives the loop ctx so
	// a graceful drain stops new claims without cancelling a turn the drain
	// budget is still waiting on; it must end no later than final teardown.
	execCtx context.Context
	// runDone closes when Run returns. The drain joins the loop rather than
	// counting in-flight claims: a claim commits its run row before any
	// counter could be incremented, so a count observed at zero can still
	// have a committed claim about to execute. Run drives ProcessOnce
	// synchronously, so once Run returns no claim can still be mid-flight
	// and no new claim can begin — that is the authoritative idle point.
	runDone chan struct{}
}

func NewWorker(db *pgxpool.Pool, workerID string, executor Executor, onFinish FinishHook, opts ...func(*Worker)) *Worker {
	w := &Worker{
		db:       db,
		runs:     New(db),
		exec:     sessionexecution.New(db),
		id:       workerID,
		executor: executor,
		onFinish: onFinish,
		poll:     500 * time.Millisecond,
		runDone:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// WithTurnAppender commits the run's deferred transcript inside the finish
// transaction — the app's lcm provider implements it.
func WithTurnAppender(fn func(ctx context.Context, tx pgx.Tx, session memory.Session, msgs []ai.Message) error) func(*Worker) {
	return func(w *Worker) { w.turnAppender = fn }
}

// WithExecContext parents claimed turns under ctx instead of the loop's stop
// context, decoupling drain lifecycles: the loop ctx ends new claims while
// the in-flight turn keeps running under the drain budget.
func WithExecContext(ctx context.Context) func(*Worker) {
	return func(w *Worker) { w.execCtx = ctx }
}

// Run is the polling loop: claim-and-execute until ctx ends, reaping expired
// leases on the way so dead workers' runs become 'interrupted' promptly. Run
// returns only after an in-flight ProcessOnce has committed its finish —
// callers needing "no claimed work remains" join on runDone via WaitInFlight.
func (w *Worker) Run(ctx context.Context) {
	defer close(w.runDone)
	reap := time.NewTicker(10 * time.Second)
	defer reap.Stop()
	poll := time.NewTicker(w.poll)
	defer poll.Stop()
	for {
		if _, err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "run worker step failed", "worker", w.id, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-reap.C:
			if n, err := w.runs.ReapExpired(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "run reaper failed", "worker", w.id, "error", err)
			} else if n > 0 {
				slog.InfoContext(ctx, "run reaper interrupted runs", "worker", w.id, "count", n)
			}
		case <-poll.C:
		}
	}
}

// ProcessOnce claims at most one queued run — run row, execution lease, run
// start and run<->lease link in one transaction — then executes it under the
// adopted lease and lets the lease's finish transaction commit the run's
// terminal state plus any reply ops. Returns whether a run was claimed.
func (w *Worker) ProcessOnce(ctx context.Context) (bool, error) {
	outcome := &runOutcome{}
	run, runCtx, lease, err := w.claim(ctx, outcome)
	if err != nil || lease == nil {
		return false, err
	}
	outcome.run = run
	done := make(chan struct{})
	go func() {
		defer close(done)
		outcome.reply, outcome.err = w.executor.Execute(runCtx, run)
	}()
	select {
	case <-ctx.Done():
		// The worker loop is stopping; the lease ctx still finishes the turn or
		// dies with the process — never cancel mid-write here.
		<-done
	case <-done:
	}
	// The activity contract only understands success/canceled/error — the
	// same vocabulary the API maps to activity_status; anything else reads
	// as an unexplained idle.
	result := "success"
	switch {
	case errors.Is(outcome.err, context.Canceled):
		result = "canceled"
	case outcome.err != nil:
		result = "error"
	case errors.Is(context.Cause(runCtx), sessionexecution.ErrLost):
		result = "error"
	}
	if err := lease.Finish(result); err != nil {
		// ErrOutcomeUnknown: the finish tx may have committed; the run must not
		// be retried — the reaper or a manual check resolves it.
		return true, fmt.Errorf("finish run %s: %w", run.ID, err)
	}
	return true, nil
}

// WaitInFlight joins the Run loop: it returns once Run has exited, which by
// construction means the claim→execute→finish span of every claimed run has
// committed and no further claim can start. A counter on ProcessOnce cannot
// express this — a claim commits its run row before any increment, so a
// zero count can hide a claim mid-flight. Run must be started first; without
// it this blocks until ctx expires.
func (w *Worker) WaitInFlight(ctx context.Context) error {
	select {
	case <-w.runDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// claimTransaction is the atomic claim: lock the run's session execution row,
// interrupt orphans, start the run, and link run<->lease before committing.
func (w *Worker) claim(ctx context.Context, outcome *runOutcome) (sqlc.AgentRun, context.Context, *sessionexecution.Lease, error) {
	opCtx, cancel := context.WithTimeout(ctx, sessionexecution.OperationTimeout)
	defer cancel()
	cands, err := sqlc.New(w.db).ListQueuedAgentRuns(opCtx)
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, err
	}
	for _, cand := range cands {
		run, runCtx, lease, ok, err := w.claimCandidate(opCtx, ctx, cand, outcome)
		if err != nil || ok {
			return run, runCtx, lease, err
		}
	}
	return sqlc.AgentRun{}, nil, nil, nil
}

// claimCandidate evaluates one queued run in its own short transaction:
// cheap head-of-line and target checks run before any lease is taken, so a
// skipped or dead candidate leaves no side effects; a dead target fails
// terminally inside its own commit.
func (w *Worker) claimCandidate(opCtx, ctx context.Context, cand sqlc.AgentRun, outcome *runOutcome) (sqlc.AgentRun, context.Context, *sessionexecution.Lease, bool, error) {
	tx, err := w.db.Begin(opCtx)
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	defer func() { _ = tx.Rollback(opCtx) }()
	q := sqlc.New(tx)

	// Per-session FIFO: never leapfrog an earlier queued predecessor, whether
	// it is locked by another worker's scan or simply earlier (CR-013).
	earlier, err := q.EarlierOpenAgentRunExists(opCtx, sqlc.EarlierOpenAgentRunExistsParams{
		SessionID:  cand.SessionID,
		EnqueueSeq: cand.EnqueueSeq,
	})
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	if earlier {
		return sqlc.AgentRun{}, nil, nil, false, nil
	}
	// Target check before the lease: an archived/gone session can never run —
	// fail the run terminally and commit it, or it wedges the queue head
	// forever (CR-009).
	ok, err := q.SessionTargetExecutable(opCtx, cand.SessionID)
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	if !ok {
		if _, ferr := q.FailQueuedAgentRun(opCtx, sqlc.FailQueuedAgentRunParams{
			ID: cand.ID, ErrorCode: pgtype.Text{String: ErrCodeTargetGone, Valid: true},
		}); ferr != nil {
			return sqlc.AgentRun{}, nil, nil, false, ferr
		}
		if err := tx.Commit(opCtx); err != nil {
			return sqlc.AgentRun{}, nil, nil, false, err
		}
		return sqlc.AgentRun{}, nil, nil, false, nil
	}

	token := uuid.Must(uuid.NewV7()).String()
	// Takeover of an expired lease demands proof the previous writer exited
	// (plan D9): a live or unverifiable owner denies the claim and the run
	// stays queued — resource unavailable, not a blind dual-writer.
	err = sessionexecution.ClaimSessionTx(opCtx, tx, cand.SessionID, token, sessionexecution.ProcessOwner(w.id))
	if errors.Is(err, agenterr.ErrSessionBusy) {
		return sqlc.AgentRun{}, nil, nil, false, nil
	}
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	// The lease was dead or absent, so any 'running' row for the session is
	// orphaned; interrupt it so the one-running index admits this run.
	if _, err := q.InterruptStaleRunningAgentRuns(opCtx, cand.SessionID); err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	if rows, err := q.StartSessionExecutionActivity(opCtx, cand.SessionID); err != nil || rows != 1 {
		// The session went away between the pre-check and the claim: roll back
		// so the freshly claimed lease row dies with this tx — a lease nobody
		// adopts would block the session head for its full TTL (CR-020). The
		// target-gone mark lands on the next sweep's pre-check.
		return sqlc.AgentRun{}, nil, nil, false, nil
	}
	if _, err := q.SetSessionExecutionRun(opCtx, sqlc.SetSessionExecutionRunParams{SessionID: cand.SessionID, Token: token, RunID: textOrNull(cand.ID)}); err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	n, err := q.StartAgentRun(opCtx, sqlc.StartAgentRunParams{ID: cand.ID, WorkerID: textOrNull(w.id)})
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, false, err
	}
	if n != 1 {
		// Lost the run race to another worker's claim; roll back and let the
		// next sweep pick up whatever remains queued.
		return sqlc.AgentRun{}, nil, nil, false, nil
	}
	if err := tx.Commit(opCtx); err != nil {
		return sqlc.AgentRun{}, nil, nil, false, fmt.Errorf("claim run %s (outcome unknown): %w", cand.ID, err)
	}
	outcome.run = cand
	outcome.turn = &agentsession.DeferredTurnStore{}
	execCtx := w.execCtx
	if execCtx == nil {
		execCtx = ctx
	}
	runCtx, lease := w.exec.Adopt(execCtx, cand.SessionID, token, w.completeExtra(outcome))
	runCtx = agentsession.WithDeferredTurnStore(runCtx, outcome.turn)
	return cand, runCtx, lease, true, nil
}

// runOutcome carries what the executor produced into the finish transaction.
type runOutcome struct {
	run   sqlc.AgentRun
	turn  *agentsession.DeferredTurnStore
	reply string
	err   error
}

func (w *Worker) completeExtra(o *runOutcome) sessionexecution.FinishExtra {
	return func(ctx context.Context, tx pgx.Tx, result string) error {
		state := StateCompleted
		switch result {
		case "success":
		case "canceled":
			state = StateCanceled
		default:
			state = StateFailed
		}
		code := ""
		if state == StateFailed {
			code = ErrCodeTurnFailed
			switch {
			case errors.Is(o.err, ErrTargetGone):
				code = ErrCodeTargetGone
			case errors.Is(context.Cause(ctx), sessionexecution.ErrLost):
				code = ErrCodeWorkerLost
			}
		}
		n, err := sqlc.New(tx).FinishAgentRun(ctx, sqlc.FinishAgentRunParams{
			ID:        o.run.ID,
			WorkerID:  textOrNull(w.id),
			State:     string(state),
			ErrorCode: textOrNull(code),
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("finish run %s fenced out", o.run.ID)
		}
		if o.turn != nil && w.turnAppender != nil {
			if rows := o.turn.Rows(); len(rows) > 0 {
				var actor Actor
				if err := json.Unmarshal(o.run.Actor, &actor); err != nil {
					return fmt.Errorf("run actor for history: %w", err)
				}
				sess := memory.Session{ID: o.run.SessionID, UserID: actor.UserID, AgentID: o.run.AgentID, GroupID: actor.GroupID, GuestID: actor.GuestID}
				if sess.UserID == "" {
					// Guest and group turns persist under their compatibility
					// owner key — mirror ScopeUserIDFromContext precedence.
					sess.UserID = actor.GuestID
				}
				if sess.UserID == "" {
					sess.UserID = actor.GroupID
				}
				if err := w.turnAppender(ctx, tx, sess, rows); err != nil {
					return fmt.Errorf("append deferred turn history: %w", err)
				}
			}
		}
		if w.onFinish != nil {
			return w.onFinish(ctx, tx, o.run, result, o.reply)
		}
		return nil
	}
}
