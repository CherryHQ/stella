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

// RunContext is the per-run domain payload handed to RunnerFunc. It carries
// only the fields a runner needs (M10) so the runner contract is decoupled
// from the sqlc-generated AgentTaskRun row layout. New fields can be added
// without breaking adapter implementations.
type RunContext struct {
	RunID           string
	TaskID          string
	OrgID           string
	UserID          string
	AgentID         string // creator agent (may be empty if task had none)
	ExecutorAgentID string // resolved executor agent for this run
	SessionID       string
	AttemptNo       int64
	Input           string // task input as JSON
}

// runContextFromRow projects the persisted run row into the domain struct.
func runContextFromRow(r sqlc.AgentTaskRun) RunContext {
	return RunContext{
		RunID:           r.ID,
		TaskID:          r.TaskID.String,
		OrgID:           r.OrgID,
		UserID:          r.UserID,
		AgentID:         r.AgentID.String,
		ExecutorAgentID: r.ExecutorAgentID.String,
		SessionID:       r.SessionID,
		AttemptNo:       r.AttemptNo,
		Input:           r.Input,
	}
}

// RunnerFunc is the agent-execution callback that the worker invokes for a
// claimed run. The runner must call exactly one of tool.Submit / tool.Block /
// tool.Fail before returning; otherwise the worker applies the protocol-error
// fallback per D14 / HP5.
//
// The runner runs inside the dispatcher's worker goroutine and must honor ctx
// cancellation (e.g. on dispatcher shutdown).
type RunnerFunc func(ctx context.Context, run RunContext, tool *TaskControlTool) error

// HeartbeatInterval is how often the worker extends lease_expires_at on the
// run row while the runner is executing. Set to 0 to disable heartbeats.
const HeartbeatInterval = 15 * time.Second

// LeaseDuration is the default lease applied while a run is in flight. Set
// well above HeartbeatInterval (M3: ~5x) so transient scheduler delays,
// GC pauses, or one missed beat don't expire the lease and let the
// dispatcher's stale-run sweep yank the task away from a still-live worker.
const LeaseDuration = 90 * time.Second

// Worker runs one claimed agent_task_run to completion.
type Worker struct {
	svc       *TransitionService
	q         *sqlc.Queries
	runner    RunnerFunc
	heartbeat time.Duration
	lease     time.Duration
	log       *slog.Logger
}

// NewWorker wires a worker.
func NewWorker(svc *TransitionService, q *sqlc.Queries, runner RunnerFunc) *Worker {
	return &Worker{
		svc:       svc,
		q:         q,
		runner:    runner,
		heartbeat: HeartbeatInterval,
		lease:     LeaseDuration,
		log:       slog.Default().With("component", "tasks/worker"),
	}
}

// SetHeartbeat overrides the heartbeat interval (for tests).
func (w *Worker) SetHeartbeat(d time.Duration) { w.heartbeat = d }

// SetLease overrides the lease duration (for tests).
func (w *Worker) SetLease(d time.Duration) { w.lease = d }

// Run drives the runner for one claimed task. Responsibilities:
//   - flip the run from queued to running with started_at + initial lease
//   - keep lease_expires_at fresh while the runner executes (heartbeat)
//   - apply the protocol-error fallback if the runner returns without a
//     terminal control action (HP5 / D14)
//   - turn runner panics into a non-retryable Fail
//
// Run does not transition the task itself; the control tool does that.
func (w *Worker) Run(ctx context.Context, taskID, runID string, actor Actor) (err error) {
	run, err := w.q.GetAgentTaskRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("worker: load run: %w", err)
	}

	now := w.svc.now()
	if _, perr := w.q.PromoteAgentTaskRun(ctx, sqlc.PromoteAgentTaskRunParams{
		StartedAt:      sql.NullString{String: now, Valid: true},
		HeartbeatAt:    sql.NullString{String: now, Valid: true},
		LeaseExpiresAt: w.leaseUntil(),
		UpdatedAt:      now,
		ID:             runID,
	}); perr != nil {
		return fmt.Errorf("worker: promote run: %w", perr)
	}

	tool := NewTaskControlTool(w.svc, w.q, taskID, runID, actor)

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
			w.log.Error("worker runner panicked", "task_id", taskID, "panic", r)
			_ = w.svc.Fail(context.Background(), FailParams{
				TaskID: taskID, RunID: runID,
				Reason: fmt.Sprintf("runner panic: %v", r), Retryable: false, Actor: actor,
			})
			err = fmt.Errorf("runner panic: %v", r)
		}
	}()

	rerr := w.runner(ctx, runContextFromRow(run), tool)

	if !tool.Finished() {
		reason := "agent exited without calling submit/block/fail"
		retryable := true
		if rerr != nil {
			reason = fmt.Sprintf("agent error: %v", rerr)
			// A ctx-cancellation means we initiated shutdown; do not consume
			// retry budget so the task picks up cleanly on next boot.
			if errors.Is(rerr, context.Canceled) {
				retryable = true
			}
		}
		// Use a fresh context: if ctx was cancelled, we still need to write
		// the failure record.
		if ferr := w.svc.Fail(context.Background(), FailParams{
			TaskID: taskID, RunID: runID, Reason: reason, Retryable: retryable, Actor: actor,
		}); ferr != nil {
			w.log.Warn("worker: fail bookkeeping returned error", "err", ferr)
		}
		w.appendProtocolError(taskID, runID, reason, actor)
		return nil
	}
	return rerr
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
			_, _ = w.q.HeartbeatAgentTaskRun(ctx, sqlc.HeartbeatAgentTaskRunParams{
				HeartbeatAt:    sql.NullString{String: w.svc.now(), Valid: true},
				LeaseExpiresAt: w.leaseUntil(),
				UpdatedAt:      w.svc.now(),
				ID:             runID,
			})
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
// Best-effort; failure to write the event must not mask the protocol error.
func (w *Worker) appendProtocolError(taskID, runID, reason string, actor Actor) {
	ctx := context.Background()
	if err := w.svc.appendEvent(ctx, w.q, sqlc.InsertAgentTaskEventParams{
		TaskID:    nullable(taskID),
		RunID:     nullable(runID),
		EventType: "protocol_error",
		ActorType: actorTypeOrSystem(actor),
		ActorID:   nullable(actor.ID),
		Detail:    detailJSON(map[string]any{"reason": reason}),
	}); err != nil {
		w.log.Warn("worker: append protocol_error event failed", "err", err)
	}
}
