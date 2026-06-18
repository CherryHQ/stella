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

// HeartbeatInterval is how often the worker extends lease_expires_at on the
// run row while the runner is executing. Set to 0 to disable heartbeats.
const HeartbeatInterval = 20 * time.Second

// LeaseDuration is the default lease applied while a run is in flight. Must
// be > 3 * HeartbeatInterval so a single missed beat doesn't expire the lease.
const LeaseDuration = 90 * time.Second

// Worker runs one claimed agent_task_run to completion.
type Worker struct {
	svc       *TransitionService
	q         *sqlc.Queries
	exec      Executor
	heartbeat time.Duration
	lease     time.Duration
	log       *slog.Logger
}

// NewWorker wires a worker.
func NewWorker(svc *TransitionService, q *sqlc.Queries, exec Executor) *Worker {
	return &Worker{
		svc:       svc,
		q:         q,
		exec:      exec,
		heartbeat: HeartbeatInterval,
		lease:     LeaseDuration,
		log:       slog.Default().With("component", "tasks/worker"),
	}
}

// SetHeartbeat overrides the heartbeat interval (for tests).
func (w *Worker) SetHeartbeat(d time.Duration) { w.heartbeat = d }

// SetLease overrides the lease duration (for tests).
func (w *Worker) SetLease(d time.Duration) { w.lease = d }

// Run drives the executor for one claimed task. Responsibilities:
//   - flip the run from queued to running with started_at + initial lease
//   - keep lease_expires_at fresh while the executor runs (heartbeat)
//   - apply exactly one terminal transition (submit/block/fail) from the
//     executor's Result at the worker boundary (D3)
//   - apply the protocol-error fallback when the executor reports no terminal
//     action (HP5 / D14)
//   - turn executor panics into a non-retryable Fail
func (w *Worker) Run(ctx context.Context, taskID, runID string, actor Actor) (err error) {
	run, err := w.q.GetAgentTaskRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("worker: load run: %w", err)
	}
	task, err := w.q.GetAgentTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("worker: load task: %w", err)
	}

	now := w.svc.now()
	promoted, perr := w.q.PromoteAgentTaskRun(ctx, sqlc.PromoteAgentTaskRunParams{
		StartedAt:      sql.NullString{String: now, Valid: true},
		HeartbeatAt:    sql.NullString{String: now, Valid: true},
		LeaseExpiresAt: w.leaseUntil(),
		UpdatedAt:      now,
		ID:             runID,
	})
	if perr != nil {
		return fmt.Errorf("worker: promote run: %w", perr)
	}
	// PromoteAgentTaskRun only matches a still-queued run. Zero rows means this
	// run was already interrupted or cancelled (lease expiry, shutdown) before
	// the worker started; abort so a superseded run never executes or applies a
	// terminal transition against a retry that re-claimed the task.
	if promoted == 0 {
		return ErrInvalidTransition
	}

	// Start heartbeat in the background.
	hbCtx, hbCancel := context.WithCancel(ctx)
	var hbWG sync.WaitGroup
	if w.heartbeat > 0 {
		hbWG.Add(1)
		go w.heartbeatLoop(hbCtx, &hbWG, runID)
	}

	defer func() {
		hbCancel()
		hbWG.Wait()
		if r := recover(); r != nil {
			w.log.Error("worker executor panicked", "task_id", taskID, "panic", r)
			_ = w.svc.Fail(context.Background(), FailParams{
				TaskID: taskID, RunID: runID,
				Reason: fmt.Sprintf("executor panic: %v", r), Retryable: false, Actor: actor,
			})
			err = fmt.Errorf("executor panic: %v", r)
		}
	}()

	res, eerr := w.exec.Execute(ctx, Request{Run: run, Task: &task})
	if eerr != nil {
		// The executor encodes outcomes in Result; a returned error is
		// unexpected. Treat it as a retryable failure so the task can recover.
		w.log.Warn("worker: executor returned error", "task_id", taskID, "run_id", runID, "err", eerr)
		res = failResult(fmt.Sprintf("executor error: %v", eerr), true)
	}
	return w.applyResult(taskID, runID, actor, res)
}

// applyResult writes the single durable transition implied by the executor's
// Result. A fresh context is used so the outcome is recorded even if the
// dispatch context was cancelled (e.g. on shutdown).
func (w *Worker) applyResult(taskID, runID string, actor Actor, res Result) error {
	ctx := context.Background()
	switch res.Action {
	case TerminalSubmit:
		err := w.svc.Submit(ctx, taskID, runID, detailJSON(res.Output), actor)
		if errors.Is(err, ErrInvalidHandoff) {
			// The submit rolled back (still running). A missing handoff is a
			// protocol miss, not task failure: retry so the agent re-submits with
			// a handoff summary, and record it for the inbox like other misses.
			reason := "plan-backed task submitted without handoff.summary"
			if ferr := w.svc.Fail(ctx, FailParams{
				TaskID: taskID, RunID: runID, Reason: reason, Retryable: true, Actor: actor,
			}); ferr != nil {
				w.log.Warn("worker: fail bookkeeping returned error", "err", ferr)
			}
			w.appendProtocolError(taskID, runID, reason, false, actor)
			return nil
		}
		return err
	case TerminalBlock:
		b := res.Blocker
		if b == nil {
			b = &BlockerResult{}
		}
		kind := b.Kind
		if kind == "" {
			kind = BlockerKindUserInput
		}
		return w.svc.Block(ctx, BlockParams{
			TaskID: taskID, Kind: kind, Question: b.Question,
			Detail: detailJSON(b.Detail), RunID: runID, Actor: actor,
		})
	case TerminalFail:
		f := res.Failure
		if f == nil {
			f = &FailureResult{Reason: "unspecified failure"}
		}
		return w.svc.Fail(ctx, FailParams{
			TaskID: taskID, RunID: runID, Reason: f.Reason, Retryable: f.Retryable, Actor: actor,
		})
	default:
		// TerminalNone: the executor ran but no terminal action fired (HP5 / D14).
		reason := "agent exited without calling submit/block/fail"
		if ferr := w.svc.Fail(ctx, FailParams{
			TaskID: taskID, RunID: runID, Reason: reason, Retryable: true, Actor: actor,
		}); ferr != nil {
			w.log.Warn("worker: fail bookkeeping returned error", "err", ferr)
		}
		w.appendProtocolError(taskID, runID, reason, res.RepairAttempted, actor)
		return nil
	}
}

func (w *Worker) heartbeatLoop(ctx context.Context, wg *sync.WaitGroup, runID string) {
	defer wg.Done()
	t := time.NewTicker(w.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := w.q.HeartbeatAgentTaskRun(ctx, sqlc.HeartbeatAgentTaskRunParams{
				HeartbeatAt:    sql.NullString{String: w.svc.now(), Valid: true},
				LeaseExpiresAt: w.leaseUntil(),
				UpdatedAt:      w.svc.now(),
				ID:             runID,
			})
			if err != nil {
				// A missed beat (e.g. SQLITE_BUSY) silently shortens the lease;
				// log it so a lease expiry can be traced to a failed heartbeat.
				w.log.Warn("worker: heartbeat write failed", "run_id", runID, "err", err)
				continue
			}
			if n == 0 {
				// The run is no longer 'running' — it was interrupted (lease
				// reaped by the dispatcher) or finalized out from under us. The
				// executor keeps going but its terminal write will lose the race.
				w.log.Warn("worker: heartbeat found no running run; lease likely already reaped", "run_id", runID)
			}
		}
	}
}

func (w *Worker) leaseUntil() sql.NullString {
	return sql.NullString{
		String: w.svc.clock().Add(w.lease).UTC().Format(time.RFC3339Nano),
		Valid:  true,
	}
}

// appendProtocolError writes one event row recording the protocol violation.
// repairAttempted distinguishes a silent miss (false) from a failed bounded
// repair turn (true). Best-effort; failure to write the event must not mask the
// protocol error.
func (w *Worker) appendProtocolError(taskID, runID, reason string, repairAttempted bool, actor Actor) {
	ctx := context.Background()
	if err := w.svc.appendEvent(ctx, w.q, sqlc.InsertAgentTaskEventParams{
		TaskID:    nullable(taskID),
		RunID:     nullable(runID),
		EventType: "protocol_error",
		ActorType: actorTypeOrSystem(actor),
		ActorID:   nullable(actor.ID),
		Detail:    detailJSON(map[string]any{"reason": reason, "repair_attempted": repairAttempted}),
	}); err != nil {
		w.log.Warn("worker: append protocol_error event failed", "err", err)
	}
}
