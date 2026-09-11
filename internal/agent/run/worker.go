package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/sessionexecution"
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
}

func NewWorker(db *pgxpool.Pool, workerID string, executor Executor, onFinish FinishHook) *Worker {
	return &Worker{
		db:       db,
		runs:     New(db),
		exec:     sessionexecution.New(db),
		id:       workerID,
		executor: executor,
		onFinish: onFinish,
		poll:     500 * time.Millisecond,
	}
}

// Run is the polling loop: claim-and-execute until ctx ends, reaping expired
// leases on the way so dead workers' runs become 'interrupted' promptly.
func (w *Worker) Run(ctx context.Context) {
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
	result := "ok"
	switch {
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

// claimTransaction is the atomic claim: lock the run's session execution row,
// interrupt orphans, start the run, and link run<->lease before committing.
func (w *Worker) claim(ctx context.Context, outcome *runOutcome) (sqlc.AgentRun, context.Context, *sessionexecution.Lease, error) {
	opCtx, cancel := context.WithTimeout(ctx, sessionexecution.OperationTimeout)
	defer cancel()
	tx, err := w.db.Begin(opCtx)
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, err
	}
	defer func() { _ = tx.Rollback(opCtx) }()
	q := sqlc.New(tx)
	cands, err := q.ListQueuedAgentRuns(opCtx)
	if err != nil {
		return sqlc.AgentRun{}, nil, nil, err
	}
	for _, cand := range cands {
		token := uuid.Must(uuid.NewV7()).String()
		if _, err := q.ClaimSessionExecution(opCtx, sqlc.ClaimSessionExecutionParams{
			SessionID: cand.SessionID,
			Token:     token,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Another worker holds a live lease for this session; try the
				// next queued run instead of failing the sweep.
				continue
			}
			return sqlc.AgentRun{}, nil, nil, err
		}
		// The lease was dead or absent, so any 'running' row for the session is
		// orphaned; interrupt it so the one-running index admits this run.
		if _, err := q.InterruptStaleRunningAgentRuns(opCtx, cand.SessionID); err != nil {
			return sqlc.AgentRun{}, nil, nil, err
		}
		if rows, err := q.StartSessionExecutionActivity(opCtx, cand.SessionID); err != nil || rows != 1 {
			return sqlc.AgentRun{}, nil, nil, fmt.Errorf("session %s unavailable for run %s", cand.SessionID, cand.ID)
		}
		if _, err := q.SetSessionExecutionRun(opCtx, sqlc.SetSessionExecutionRunParams{SessionID: cand.SessionID, Token: token, RunID: textOrNull(cand.ID)}); err != nil {
			return sqlc.AgentRun{}, nil, nil, err
		}
		n, err := q.StartAgentRun(opCtx, sqlc.StartAgentRunParams{ID: cand.ID, WorkerID: textOrNull(w.id)})
		if err != nil {
			return sqlc.AgentRun{}, nil, nil, err
		}
		if n != 1 {
			// Lost the run race to another worker's claim; roll back and let
			// the next sweep pick up whatever remains queued.
			return sqlc.AgentRun{}, nil, nil, nil
		}
		if err := tx.Commit(opCtx); err != nil {
			return sqlc.AgentRun{}, nil, nil, fmt.Errorf("claim run %s (outcome unknown): %w", cand.ID, err)
		}
		outcome.run = cand
		runCtx, lease := w.exec.Adopt(ctx, cand.SessionID, token, w.completeExtra(outcome))
		return cand, runCtx, lease, nil
	}
	return sqlc.AgentRun{}, nil, nil, nil
}

// runOutcome carries what the executor produced into the finish transaction.
type runOutcome struct {
	run   sqlc.AgentRun
	reply string
	err   error
}

func (w *Worker) completeExtra(o *runOutcome) sessionexecution.FinishExtra {
	return func(ctx context.Context, tx pgx.Tx, result string) error {
		state := StateCompleted
		switch result {
		case "ok":
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
		if w.onFinish != nil {
			return w.onFinish(ctx, tx, o.run, result, o.reply)
		}
		return nil
	}
}
