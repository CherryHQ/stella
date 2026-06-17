package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// TransitionService is the single entry point for every state change on
// agent_task / agent_task_run / agent_task_blocker / agent_task_dep rows.
// Every method runs in one transaction: load → validate from→to → side
// effects → update → append event → commit. Callers branch on the typed
// errors declared in types.go.
type TransitionService struct {
	db    *sql.DB
	q     *sqlc.Queries
	clock func() time.Time
}

// NewTransitionService constructs a transition service from a *sql.DB and a
// pre-built *sqlc.Queries. The Queries is used only for non-transactional
// reads; mutating methods open their own txns and call WithTx.
func NewTransitionService(db *sql.DB, q *sqlc.Queries) *TransitionService {
	return &TransitionService{db: db, q: q, clock: time.Now}
}

// SetClock overrides the clock for tests.
func (s *TransitionService) SetClock(c func() time.Time) { s.clock = c }

func (s *TransitionService) now() time.Time {
	return s.clock().UTC()
}

func nullable(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

// withTx runs fn inside a transaction. On error it rolls back; on success it
// commits. It does not retry — SQLite serialisation is the caller's concern.
func (s *TransitionService) withTx(ctx context.Context, fn func(*sqlc.Queries) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if cerr := tx.Commit(); cerr != nil {
		err = fmt.Errorf("commit: %w", cerr)
	}
	return err
}

// appendEvent writes one audit row. event is mutated to fill in id/created_at
// if blank.
func (s *TransitionService) appendEvent(ctx context.Context, q *sqlc.Queries, e sqlc.InsertAgentTaskEventParams) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.now()
	}
	if e.ActorType == "" {
		e.ActorType = ActorSystem
	}
	if e.Detail == "" {
		e.Detail = "{}"
	}
	_, err := q.InsertAgentTaskEvent(ctx, e)
	return err
}

// detailJSON marshals an arbitrary value as the event detail blob. Empty map
// becomes "{}".
func detailJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// expect resolves the task row by id and rejects if its current status is not
// in `from`. Used at the top of every transition to make races visible.
func expect(ctx context.Context, q *sqlc.Queries, taskID string, from ...string) (sqlc.AgentTask, error) {
	t, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.AgentTask{}, ErrTaskNotFound
		}
		return sqlc.AgentTask{}, err
	}
	if slices.Contains(from, t.Status) {
		return t, nil
	}
	return t, ErrInvalidTransition
}

// runIdentityMatches fences a worker-origin terminal transition to the task's
// current run. A non-empty runID must equal task.active_run_id while the task
// is running; this rejects a stale worker whose run was interrupted (lease
// expiry, shutdown) and superseded by a retry that re-claimed the task. An
// empty runID denotes a system-origin transition (dep-failure propagation on a
// never-run task), which carries no run to match and is always allowed.
func runIdentityMatches(task sqlc.AgentTask, runID string) bool {
	if runID == "" {
		return true
	}
	return task.Status == StatusRunning &&
		task.ActiveRunID.Valid && task.ActiveRunID.String == runID
}

// ---------------------------------------------------------------------------
// Task transitions
// ---------------------------------------------------------------------------

// Activate moves StatusDraft → StatusReady.
func (s *TransitionService) Activate(ctx context.Context, taskID string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		return s.activateInTx(ctx, q, taskID, actor)
	})
}

// activateInTx runs Activate's body against an existing tx-scoped Queries.
func (s *TransitionService) activateInTx(ctx context.Context, q *sqlc.Queries, taskID string, actor Actor) error {
	if _, err := expect(ctx, q, taskID, StatusDraft); err != nil {
		return err
	}
	now := s.now()
	n, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
		Status: StatusReady, UpdatedAt: now, ID: taskID, Status_2: StatusDraft,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidTransition
	}
	return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		TaskID:     nullable(taskID),
		EventType:  "activate",
		FromStatus: nullable(StatusDraft),
		ToStatus:   nullable(StatusReady),
		ActorType:  actor.Type,
		ActorID:    nullable(actor.ID),
	})
}

// ClaimResult describes the run that was minted by Claim. SessionID is the
// task's durable worker session.
type ClaimResult struct {
	RunID     string
	SessionID string
}

// ClaimParams are the inputs the dispatcher provides when it has decided a
// task is dispatchable.
type ClaimParams struct {
	TaskID          string
	ExecutorAgentID string
	WorkerID        string
	LeaseDuration   time.Duration // 0 => no lease
	SessionID       string        // ignored; task.session_id is the source of truth
	Actor           Actor
	// HintID, when set, identifies the live dispatch hint the dispatcher used
	// to resolve ExecutorAgentID. Claim consumes that exact hint inside its
	// tx so the resolved executor and the consumed hint can't diverge under
	// concurrent hint replacement. Empty => no hint to consume.
	HintID string
}

// Claim atomically moves StatusReady → StatusRunning AND inserts a new
// agent_task_run row, setting task.active_run_id in the same transaction.
// Returns ErrInvalidTransition if the row's status changed underfoot — the
// caller (dispatcher) should re-scan candidates and try again.
//
// Session-id rule: task.session_id is required at creation time and reused for every run.
func (s *TransitionService) Claim(ctx context.Context, p ClaimParams) (ClaimResult, error) {
	if p.TaskID == "" {
		return ClaimResult{}, fmt.Errorf("Claim: task id required")
	}
	var out ClaimResult
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := q.GetAgentTask(ctx, p.TaskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		if task.Status != StatusReady {
			return ErrInvalidTransition
		}
		// Mint a new run row first. attempt_no is derived from prior worker runs.
		nextAttempt, err := q.NextAttemptNoForTask(ctx, sqlc.NextAttemptNoForTaskParams{
			TaskID: nullable(p.TaskID), Kind: RunKindWorker,
		})
		if err != nil {
			return fmt.Errorf("next attempt: %w", err)
		}
		runID := uuid.NewString()
		now := s.now()
		var leaseExpires sql.NullTime
		if p.LeaseDuration > 0 {
			leaseExpires = sql.NullTime{Time: s.clock().Add(p.LeaseDuration).UTC(), Valid: true}
		}
		if task.SessionID == "" {
			return fmt.Errorf("Claim: task has no worker session")
		}
		sessionID := task.SessionID
		_, err = q.CreateAgentTaskRun(ctx, sqlc.CreateAgentTaskRunParams{
			ID:              runID,
			TaskID:          nullable(p.TaskID),
			GoalID:          sql.NullString{},
			UserID:          task.UserID,
			AgentID:         nullable(task.AgentID),
			ExecutorAgentID: nullable(p.ExecutorAgentID),
			Kind:            RunKindWorker,
			AttemptNo:       int64(nextAttempt),
			Status:          RunQueued,
			SessionID:       sessionID,
			Input:           "{}",
			LeaseExpiresAt:  leaseExpires,
			WorkerID:        p.WorkerID,
			// Queued runs have no start time; worker PromoteAgentTaskRun
			// stamps started_at on the queued->running transition.
			StartedAt: sql.NullTime{},
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		// Atomic claim: ready + no active run -> running, set active_run_id.
		n, err := q.ClaimAgentTask(ctx, sqlc.ClaimAgentTaskParams{
			ActiveRunID: nullable(runID),
			UpdatedAt:   now,
			ID:          p.TaskID,
		})
		if err != nil {
			return fmt.Errorf("claim update: %w", err)
		}
		if n == 0 {
			// Another tick beat us. Rolling back the tx unwinds the run insert too.
			return ErrInvalidTransition
		}
		// Consume the dispatch hint the dispatcher resolved against (B1). The
		// ConsumeDispatchHint query guards on consumed_at IS NULL, so a hint
		// replaced underfoot leaves the new hint untouched.
		if p.HintID != "" {
			_, _ = q.ConsumeDispatchHint(ctx, sqlc.ConsumeDispatchHintParams{
				ConsumedAt: sql.NullTime{Time: now, Valid: true}, ID: p.HintID,
			})
		}
		if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(p.TaskID),
			RunID:      nullable(runID),
			EventType:  "claim",
			FromStatus: nullable(StatusReady),
			ToStatus:   nullable(StatusRunning),
			ActorType:  actorTypeOrSystem(p.Actor),
			ActorID:    nullable(p.Actor.ID),
			Detail:     detailJSON(map[string]any{"attempt_no": nextAttempt, "executor_agent_id": p.ExecutorAgentID}),
		}); err != nil {
			return err
		}
		out = ClaimResult{RunID: runID, SessionID: sessionID}
		return nil
	})
	return out, err
}

// BlockParams captures the fields needed to record a new blocker.
type BlockParams struct {
	TaskID   string
	Kind     string
	Question string
	Detail   string
	RunID    string // run that triggered the block; empty for system-created
	Actor    Actor
}

// Block transitions a task to StatusBlocked and inserts a blocker row. If
// the task already has an open blocker, the new condition is merged into the
// existing blocker's detail instead of inserting a second open row (D14 / H4).
func (s *TransitionService) Block(ctx context.Context, p BlockParams) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := q.GetAgentTask(ctx, p.TaskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		now := s.now()

		// Merge path takes precedence over status check: a task already in
		// StatusBlocked has an open blocker, and we merge the new condition
		// into it. This is the only case where Block accepts from=blocked.
		if open, err := q.GetOpenBlockerForTask(ctx, p.TaskID); err == nil {
			merged := mergeBlockerDetail(open.Detail, p.Kind, p.Question, p.Detail)
			if err := q.AppendAgentTaskBlockerDetail(ctx, sqlc.AppendAgentTaskBlockerDetailParams{
				Detail: merged, ID: open.ID,
			}); err != nil {
				return err
			}
			return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
				TaskID:    nullable(p.TaskID),
				BlockerID: nullable(open.ID),
				EventType: "blocker_merged",
				ActorType: actorTypeOrSystem(p.Actor),
				ActorID:   nullable(p.Actor.ID),
				Detail:    detailJSON(map[string]any{"new_kind": p.Kind, "question": p.Question}),
			})
		}

		switch task.Status {
		case StatusReady, StatusRunning:
			// ok
		default:
			return ErrInvalidTransition
		}
		if !runIdentityMatches(task, p.RunID) {
			return ErrInvalidTransition
		}

		// Insert new blocker + transition.
		blockerID := uuid.NewString()
		detailStr := p.Detail
		if detailStr == "" {
			detailStr = "{}"
		}
		if _, err := q.CreateAgentTaskBlocker(ctx, sqlc.CreateAgentTaskBlockerParams{
			ID:             blockerID,
			TaskID:         p.TaskID,
			Kind:           p.Kind,
			Status:         BlockerOpen,
			Question:       p.Question,
			Detail:         detailStr,
			CreatedByRunID: nullable(p.RunID),
			CreatedAt:      now,
		}); err != nil {
			return fmt.Errorf("create blocker: %w", err)
		}
		from := task.Status
		n, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: StatusBlocked, UpdatedAt: now, ID: p.TaskID, Status_2: from,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		if err := q.SetAgentTaskActiveBlocker(ctx, sqlc.SetAgentTaskActiveBlockerParams{
			ActiveBlockerID: nullable(blockerID), UpdatedAt: now, ID: p.TaskID,
		}); err != nil {
			return err
		}
		// If we were running, clear active_run_id and mark its run cancelled.
		if from == StatusRunning && task.ActiveRunID.Valid {
			if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: RunCancelled, Result: "{}", Error: "blocked",
				FinishedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt:  now, ID: task.ActiveRunID.String,
			}); err != nil {
				return err
			}
			if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
				ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: p.TaskID,
			}); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(p.TaskID),
			BlockerID:  nullable(blockerID),
			RunID:      nullable(p.RunID),
			EventType:  "block",
			FromStatus: nullable(from),
			ToStatus:   nullable(StatusBlocked),
			ActorType:  actorTypeOrSystem(p.Actor),
			ActorID:    nullable(p.Actor.ID),
			Detail:     detailJSON(map[string]any{"kind": p.Kind, "question": p.Question}),
		})
	})
}

// mergeBlockerDetail composes an updated JSON blob that preserves the
// original detail and appends a "merged" array with the new condition.
func mergeBlockerDetail(existing, newKind, newQuestion, newDetail string) string {
	var doc map[string]any
	if existing == "" || existing == "{}" {
		doc = map[string]any{}
	} else {
		_ = json.Unmarshal([]byte(existing), &doc)
		if doc == nil {
			doc = map[string]any{}
		}
	}
	merged, _ := doc["merged"].([]any)
	merged = append(merged, map[string]any{
		"kind":     newKind,
		"question": newQuestion,
		"detail":   newDetail,
	})
	doc["merged"] = merged
	b, _ := json.Marshal(doc)
	return string(b)
}

// ResolveBlocker closes an open blocker and moves its task back to
// StatusReady. dep_failure blockers cannot be resolved through this path —
// callers must use WaiveDep (D14 / M1); we surface this with
// ErrDepFailureUnresolved so the handler can return 409 with the right
// message.
func (s *TransitionService) ResolveBlocker(ctx context.Context, blockerID, resolution string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		blocker, err := q.GetAgentTaskBlocker(ctx, blockerID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrBlockerNotFound
			}
			return err
		}
		if blocker.Status != BlockerOpen {
			return ErrAlreadyClosed
		}
		if blocker.Kind == BlockerKindDepFailure {
			return ErrDepFailureUnresolved
		}
		now := s.now()
		if resolution == "" {
			resolution = "{}"
		}
		n, err := q.ResolveAgentTaskBlocker(ctx, sqlc.ResolveAgentTaskBlockerParams{
			Resolution: resolution,
			ResolvedAt: sql.NullTime{Time: now, Valid: true},
			ID:         blockerID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrAlreadyClosed
		}
		taskID := blocker.TaskID
		nn, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: StatusReady, UpdatedAt: now, ID: taskID, Status_2: StatusBlocked,
		})
		if err != nil {
			return err
		}
		if nn == 0 {
			return ErrInvalidTransition
		}
		if err := q.SetAgentTaskActiveBlocker(ctx, sqlc.SetAgentTaskActiveBlockerParams{
			ActiveBlockerID: sql.NullString{}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(taskID),
			BlockerID:  nullable(blockerID),
			EventType:  "blocker_resolved",
			FromStatus: nullable(StatusBlocked),
			ToStatus:   nullable(StatusReady),
			ActorType:  actorTypeOrSystem(actor),
			ActorID:    nullable(actor.ID),
		})
	})
}

// WaiveDep records a waiver on a hard-dep edge whose upstream failed or
// cancelled. After waiver, readiness treats the edge as satisfied; the
// downstream's dep_failure blocker (if any) is also resolved in the same tx.
func (s *TransitionService) WaiveDep(ctx context.Context, taskID, depTaskID, userID, reason string, actor Actor) error {
	if reason == "" {
		return fmt.Errorf("WaiveDep: reason is required")
	}
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		now := s.now()
		n, err := q.WaiveAgentTaskDep(ctx, sqlc.WaiveAgentTaskDepParams{
			WaivedAt:     sql.NullTime{Time: now, Valid: true},
			WaivedByUser: nullable(userID),
			WaiverReason: reason,
			TaskID:       taskID,
			DepTaskID:    depTaskID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("WaiveDep: dep edge not found or already waived")
		}
		// If the downstream is blocked with a dep_failure blocker, resolve it
		// and return the task to ready.
		if open, err := q.GetOpenBlockerForTask(ctx, taskID); err == nil && open.Kind == BlockerKindDepFailure {
			if _, err := q.ResolveAgentTaskBlocker(ctx, sqlc.ResolveAgentTaskBlockerParams{
				Resolution: detailJSON(map[string]any{"waived_dep_task_id": depTaskID, "reason": reason}),
				ResolvedAt: sql.NullTime{Time: now, Valid: true},
				ID:         open.ID,
			}); err != nil {
				return err
			}
			if _, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
				Status: StatusReady, UpdatedAt: now, ID: taskID, Status_2: StatusBlocked,
			}); err != nil {
				return err
			}
			if err := q.SetAgentTaskActiveBlocker(ctx, sqlc.SetAgentTaskActiveBlockerParams{
				ActiveBlockerID: sql.NullString{}, UpdatedAt: now, ID: taskID,
			}); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:    nullable(taskID),
			EventType: "dep_waived",
			ActorType: actorTypeOrSystem(actor),
			ActorID:   nullable(actor.ID),
			Detail:    detailJSON(map[string]any{"dep_task_id": depTaskID, "reason": reason}),
		})
	})
}

// FailParams captures a worker-or-system-declared run failure.
type FailParams struct {
	TaskID          string
	RunID           string
	Reason          string // free-form error message
	Retryable       bool   // if false, force terminal failure
	RunStatusOnFail string // override final run status; "" => RunFailed
	Actor           Actor
}

// Fail records a run failure and either returns the task to StatusReady (if
// the retry budget allows and Retryable is true) or moves it to StatusFailed.
func (s *TransitionService) Fail(ctx context.Context, p FailParams) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := q.GetAgentTask(ctx, p.TaskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		if task.Status != StatusRunning && task.Status != StatusReady {
			return ErrInvalidTransition
		}
		if !runIdentityMatches(task, p.RunID) {
			return ErrInvalidTransition
		}
		now := s.now()
		// Finalize the run row, if provided. Caller may override the final
		// run status (e.g. dispatcher's stale-run sweep uses RunInterrupted).
		runStatus := p.RunStatusOnFail
		if runStatus == "" {
			runStatus = RunFailed
		}
		if p.RunID != "" {
			if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: runStatus, Result: "{}", Error: p.Reason,
				FinishedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt:  now, ID: p.RunID,
			}); err != nil {
				return err
			}
		}
		// Decide downstream state.
		next := StatusReady
		var budgetExhausted bool
		if !p.Retryable || task.RetryCount+1 > task.MaxRetries {
			next = StatusFailed
			budgetExhausted = !p.Retryable || task.RetryCount+1 > task.MaxRetries
		}
		// Only increment retry on a retryable failure; terminal failures
		// don't consume a retry slot.
		if next == StatusReady {
			if err := q.IncrementAgentTaskRetry(ctx, sqlc.IncrementAgentTaskRetryParams{
				UpdatedAt: now, ID: p.TaskID,
			}); err != nil {
				return err
			}
		}
		n, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: next, UpdatedAt: now, ID: p.TaskID, Status_2: task.Status,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		// Clear active_run_id either way.
		if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
			ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: p.TaskID,
		}); err != nil {
			return err
		}
		evt := "fail_retry"
		if next == StatusFailed {
			evt = "fail"
		}
		if err := s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(p.TaskID),
			RunID:      nullable(p.RunID),
			EventType:  evt,
			FromStatus: nullable(task.Status),
			ToStatus:   nullable(next),
			ActorType:  actorTypeOrSystem(p.Actor),
			ActorID:    nullable(p.Actor.ID),
			Detail:     detailJSON(map[string]any{"reason": p.Reason, "retryable": p.Retryable, "retry_count": task.RetryCount}),
		}); err != nil {
			return err
		}
		if budgetExhausted && p.Retryable {
			// Signal so callers can log; not a hard error.
			return nil
		}
		return nil
	})
}

// Cancel moves any non-terminal task to StatusCancelled. Idempotent: a second
// Cancel on an already-cancelled task returns nil. The dispatcher tick also
// cancels the active run if one exists.
func (s *TransitionService) Cancel(ctx context.Context, taskID, reason string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		if task.Status == StatusCancelled {
			return nil
		}
		if IsTerminalStatus(task.Status) {
			return ErrInvalidTransition
		}
		now := s.now()
		n, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: StatusCancelled, UpdatedAt: now, ID: taskID, Status_2: task.Status,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		if err := q.SetAgentTaskCancelled(ctx, sqlc.SetAgentTaskCancelledParams{
			CancelledAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		// Cancel an in-flight run, clear active pointers.
		if task.ActiveRunID.Valid {
			if err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: RunCancelled, Result: "{}", Error: reason,
				FinishedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt:  now, ID: task.ActiveRunID.String,
			}); err != nil {
				return err
			}
			if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
				ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: taskID,
			}); err != nil {
				return err
			}
		}
		if task.ActiveBlockerID.Valid {
			if _, err := q.CancelAgentTaskBlocker(ctx, sqlc.CancelAgentTaskBlockerParams{
				ResolvedAt: sql.NullTime{Time: now, Valid: true}, ID: task.ActiveBlockerID.String,
			}); err != nil {
				return err
			}
			if err := q.SetAgentTaskActiveBlocker(ctx, sqlc.SetAgentTaskActiveBlockerParams{
				ActiveBlockerID: sql.NullString{}, UpdatedAt: now, ID: taskID,
			}); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID:     nullable(taskID),
			EventType:  "cancel",
			FromStatus: nullable(task.Status),
			ToStatus:   nullable(StatusCancelled),
			ActorType:  actorTypeOrSystem(actor),
			ActorID:    nullable(actor.ID),
			Detail:     detailJSON(map[string]any{"reason": reason}),
		})
	})
}

// AddDep inserts a dep edge after a DFS cycle check. depKind and onFailure
// default to "hard" / "block" when blank.
func (s *TransitionService) AddDep(ctx context.Context, taskID, depTaskID, depKind, onFailure string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		return s.addDepInTx(ctx, q, taskID, depTaskID, depKind, onFailure)
	})
}

// addDepInTx runs AddDep's body against an existing tx-scoped Queries.
func (s *TransitionService) addDepInTx(ctx context.Context, q *sqlc.Queries, taskID, depTaskID, depKind, onFailure string) error {
	if taskID == "" || depTaskID == "" {
		return fmt.Errorf("AddDep: task and dep ids required")
	}
	if taskID == depTaskID {
		return ErrCycle
	}
	if depKind == "" {
		depKind = DepKindHard
	}
	if !validDepKind(depKind) {
		return fmt.Errorf("AddDep: invalid dep_kind %q", depKind)
	}
	if onFailure == "" {
		onFailure = OnFailureBlock
	}
	if !validOnFailure(onFailure) {
		return fmt.Errorf("AddDep: invalid on_failure %q", onFailure)
	}
	if reaches, err := reachable(ctx, q, depTaskID, taskID); err != nil {
		return err
	} else if reaches {
		return ErrCycle
	}
	_, err := q.CreateAgentTaskDep(ctx, sqlc.CreateAgentTaskDepParams{
		TaskID:    taskID,
		DepTaskID: depTaskID,
		DepKind:   depKind,
		OnFailure: onFailure,
		CreatedAt: s.now(),
	})
	return err
}

// WithTx exposes the transition service's transaction helper so higher-level
// composers (e.g. ServiceFacade.CreateTask) can group several mutations into
// one atomic unit while still routing status writes through tx-aware helpers.
func (s *TransitionService) WithTx(ctx context.Context, fn func(*sqlc.Queries) error) error {
	return s.withTx(ctx, fn)
}

// AddDepInTx is the exported form of addDepInTx for use inside WithTx.
func (s *TransitionService) AddDepInTx(ctx context.Context, q *sqlc.Queries, taskID, depTaskID, depKind, onFailure string) error {
	return s.addDepInTx(ctx, q, taskID, depTaskID, depKind, onFailure)
}

// ActivateInTx is the exported form of activateInTx for use inside WithTx.
func (s *TransitionService) ActivateInTx(ctx context.Context, q *sqlc.Queries, taskID string, actor Actor) error {
	return s.activateInTx(ctx, q, taskID, actor)
}

// reachable reports whether `from` can reach `target` by following dep edges
// (from -> dep_task_id -> dep_task_id -> ...). The walk is bounded by the
// number of nodes to defend against accidental existing cycles.
func reachable(ctx context.Context, q *sqlc.Queries, from, target string) (bool, error) {
	stack := []string{from}
	seen := map[string]struct{}{from: {}}
	const maxVisits = 10000
	for visits := 0; len(stack) > 0 && visits < maxVisits; visits++ {
		n := len(stack) - 1
		node := stack[n]
		stack = stack[:n]
		if node == target {
			return true, nil
		}
		edges, err := q.ListAgentTaskDeps(ctx, node)
		if err != nil {
			return false, err
		}
		for _, e := range edges {
			if _, ok := seen[e.DepTaskID]; ok {
				continue
			}
			seen[e.DepTaskID] = struct{}{}
			stack = append(stack, e.DepTaskID)
		}
	}
	return false, nil
}

func actorTypeOrSystem(a Actor) string {
	if a.Type == "" {
		return ActorSystem
	}
	return a.Type
}
