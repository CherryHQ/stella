package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// isUniqueConstraintErr reports whether err is a sqlite UNIQUE constraint
// violation. We string-match because the modernc driver does not export a
// constraint sentinel; the message text is stable across modernc/mattn.
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

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
// reads; mutating methods open their own txns via withTx.
func NewTransitionService(db *sql.DB, q *sqlc.Queries) *TransitionService {
	return &TransitionService{db: db, q: q, clock: time.Now}
}

// SetClock overrides the clock for tests.
func (s *TransitionService) SetClock(c func() time.Time) { s.clock = c }

func (s *TransitionService) now() string {
	return s.clock().UTC().Format(time.RFC3339Nano)
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
	if e.CreatedAt == "" {
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

// ---------------------------------------------------------------------------
// Task transitions
// ---------------------------------------------------------------------------

// Activate moves StatusDraft → StatusReady.
func (s *TransitionService) Activate(ctx context.Context, taskID string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		return s.activateTx(ctx, q, taskID, actor)
	})
}

// activateTx is the inner body of Activate, callable from inside an existing
// tx (CreateTask uses this to atomically draft→ready before commit).
func (s *TransitionService) activateTx(ctx context.Context, q *sqlc.Queries, taskID string, actor Actor) error {
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

// CreateTaskAtomicParams carries the fully-validated inputs to
// CreateTaskAtomic. The facade is responsible for defaulting, normalising,
// and minting the ID + timestamp; this method is the boundary that owns
// "draft row + cross-org agent checks + deps + dispatch hint + optional
// activate" as one transaction. Exposing only this verb keeps callers from
// reaching the raw *sqlc.Queries through a generic WithTx (D14).
type CreateTaskAtomicParams struct {
	ID               string
	OrgID            string
	UserID           string
	AgentID          string
	ExecutorAgentID  string
	Title            string
	Description      string
	Priority         string
	Required         int64
	MaxRetries       int64
	NotBefore        sql.NullString
	DeadlineAt       sql.NullString
	Context          string
	Now              string
	Deps             []CreateTaskAtomicDep
	ActivateOnCreate bool
}

// CreateTaskAtomicDep mirrors a single dep edge to insert with the task.
type CreateTaskAtomicDep struct {
	DepTaskID string
	Kind      string
	OnFailure string
}

// CreateTaskAtomic creates the task row, validates that AgentID /
// ExecutorAgentID belong to OrgID (H2), inserts dep edges with cycle checks
// (H1), writes a dispatch hint if ExecutorAgentID is set, and optionally
// promotes draft → ready — all in one transaction.
func (s *TransitionService) CreateTaskAtomic(ctx context.Context, p CreateTaskAtomicParams) (sqlc.AgentTask, error) {
	var out sqlc.AgentTask
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		if p.AgentID != "" {
			if _, err := q.GetAgent(ctx, sqlc.GetAgentParams{ID: p.AgentID, OrgID: p.OrgID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("CreateTask: agent_id: %w", ErrCrossOrg)
				}
				return fmt.Errorf("CreateTask: lookup agent: %w", err)
			}
		}
		if p.ExecutorAgentID != "" {
			if _, err := q.GetAgent(ctx, sqlc.GetAgentParams{ID: p.ExecutorAgentID, OrgID: p.OrgID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("CreateTask: executor_agent_id: %w", ErrCrossOrg)
				}
				return fmt.Errorf("CreateTask: lookup executor agent: %w", err)
			}
		}
		task, err := q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
			ID:          p.ID,
			OrgID:       p.OrgID,
			UserID:      p.UserID,
			AgentID:     nullable(p.AgentID),
			Title:       p.Title,
			Description: p.Description,
			Status:      StatusDraft,
			Priority:    p.Priority,
			Required:    p.Required,
			RetryCount:  0,
			MaxRetries:  p.MaxRetries,
			NotBefore:   p.NotBefore,
			DeadlineAt:  p.DeadlineAt,
			Context:     p.Context,
			Output:      "{}",
			CreatedAt:   p.Now,
			UpdatedAt:   p.Now,
		})
		if err != nil {
			return fmt.Errorf("CreateTask: %w", err)
		}
		for _, d := range p.Deps {
			if p.ID == d.DepTaskID {
				return ErrCycle
			}
			if err := s.addDepTx(ctx, q, p.ID, d.DepTaskID, d.Kind, d.OnFailure); err != nil {
				return fmt.Errorf("CreateTask: dep %s: %w", d.DepTaskID, err)
			}
		}
		if p.ExecutorAgentID != "" {
			if _, err := q.CreateAgentTaskDispatchHint(ctx, sqlc.CreateAgentTaskDispatchHintParams{
				ID:              uuid.NewString(),
				TaskID:          p.ID,
				Kind:            RunKindWorker,
				ExecutorAgentID: p.ExecutorAgentID,
				CreatedAt:       p.Now,
			}); err != nil {
				return fmt.Errorf("CreateTask: dispatch hint: %w", err)
			}
		}
		if p.ActivateOnCreate {
			if err := s.activateTx(ctx, q, p.ID, Actor{Type: ActorUser, ID: p.UserID}); err != nil {
				return fmt.Errorf("CreateTask: activate: %w", err)
			}
			task.Status = StatusReady
		}
		out = task
		return nil
	})
	if err != nil {
		return sqlc.AgentTask{}, err
	}
	return out, nil
}

// ClaimResult describes the run that was minted by Claim. SessionID is the
// session the worker should run in; on a first claim the dispatcher minted a
// fresh one, on a retry it's the task's persisted session.
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
	NewSessionID    string        // used when task.session_id is null
	Actor           Actor
}

// Claim atomically moves StatusReady → StatusRunning AND inserts a new
// agent_task_run row, setting task.active_run_id in the same transaction.
// Returns ErrInvalidTransition if the row's status changed underfoot — the
// caller (dispatcher) should re-scan candidates and try again.
//
// Session-id rule (D12): if task.session_id is non-null, reuse it; otherwise
// adopt p.NewSessionID and persist it on the task row.
func (s *TransitionService) Claim(ctx context.Context, p ClaimParams) (ClaimResult, error) {
	if p.TaskID == "" {
		return ClaimResult{}, fmt.Errorf("Claim: task id required")
	}
	if p.NewSessionID == "" {
		return ClaimResult{}, fmt.Errorf("Claim: new session id required")
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
		var leaseExpires sql.NullString
		if p.LeaseDuration > 0 {
			leaseExpires = sql.NullString{String: s.clock().Add(p.LeaseDuration).UTC().Format(time.RFC3339Nano), Valid: true}
		}
		// Choose the session per D12 before inserting the run.
		sessionID := p.NewSessionID
		if task.SessionID.Valid && task.SessionID.String != "" {
			sessionID = task.SessionID.String
		}
		_, err = q.CreateAgentTaskRun(ctx, sqlc.CreateAgentTaskRunParams{
			ID:              runID,
			TaskID:          nullable(p.TaskID),
			OrgID:           task.OrgID,
			UserID:          task.UserID,
			AgentID:         task.AgentID,
			ExecutorAgentID: nullable(p.ExecutorAgentID),
			Kind:            RunKindWorker,
			AttemptNo:       nextAttempt,
			Status:          RunQueued,
			SessionID:       sessionID,
			Input:           "{}",
			LeaseExpiresAt:  leaseExpires,
			WorkerID:        p.WorkerID,
			StartedAt:       sql.NullString{String: now, Valid: true},
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if err != nil {
			// Race loser: the partial unique index uniq_active_worker_run
			// rejects a second queued/running run for the same task. Surface
			// as ErrInvalidTransition so the dispatcher's normal "lost race"
			// path consumes it without a warning log (M1).
			if isUniqueConstraintErr(err) {
				return ErrInvalidTransition
			}
			return fmt.Errorf("create run: %w", err)
		}
		// Atomic claim: ready + no active run -> running, set active_run_id and
		// persist session if first time.
		n, err := q.ClaimAgentTask(ctx, sqlc.ClaimAgentTaskParams{
			ActiveRunID: nullable(runID),
			SessionID:   nullable(sessionID),
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
		// Consume any live dispatch hint (B1). Best-effort — if no hint was
		// live, there's nothing to consume.
		if hint, err := q.GetLiveDispatchHintForTask(ctx, sqlc.GetLiveDispatchHintForTaskParams{
			TaskID: p.TaskID, Kind: RunKindWorker,
		}); err == nil {
			_, _ = q.ConsumeDispatchHint(ctx, sqlc.ConsumeDispatchHintParams{
				ConsumedAt: sql.NullString{String: now, Valid: true}, ID: hint.ID,
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

// Submit handles a worker's submit action. Slice 1 short-circuits straight to
// StatusDone (no review pipeline). Slice 2 will branch on review_policy.
func (s *TransitionService) Submit(ctx context.Context, taskID, runID, output string, actor Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := expect(ctx, q, taskID, StatusRunning)
		if err != nil {
			return err
		}
		// Stale-worker guard: the task must still be pointing at this run.
		// After lease expiry the sweep can interrupt R1, return the task to
		// ready, and the dispatcher claims R2. A late Submit from R1's
		// worker would otherwise pass the StatusRunning check (R2 is
		// running) and overwrite R2's output. Reject when the pointer has
		// moved.
		if runID != "" && (!task.ActiveRunID.Valid || task.ActiveRunID.String != runID) {
			return ErrInvalidTransition
		}
		now := s.now()
		n, err := q.TransitionAgentTaskStatus(ctx, sqlc.TransitionAgentTaskStatusParams{
			Status: StatusDone, UpdatedAt: now, ID: taskID, Status_2: StatusRunning,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTransition
		}
		if output != "" {
			if err := q.SetAgentTaskOutput(ctx, sqlc.SetAgentTaskOutputParams{
				Output: output, CompletedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: taskID,
			}); err != nil {
				return err
			}
		}
		// Finalize the run row. Conditional on still-in-flight; if a racing
		// Cancel beat us here the run is already cancelled and we leave it.
		if runID != "" {
			if _, err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: RunCompleted, Result: output, Error: "",
				FinishedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:  now, ID: runID,
			}); err != nil {
				return err
			}
		}
		// Clear active_run_id now that the worker has finalised.
		if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
			ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			TaskID: nullable(taskID), RunID: nullable(runID),
			EventType:  "submit",
			FromStatus: nullable(StatusRunning), ToStatus: nullable(StatusDone),
			ActorType: actorTypeOrSystem(actor), ActorID: nullable(actor.ID),
		})
	})
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
			// H4: dep_failure is sticky. If the incoming condition is a
			// dep_failure (the only kind ResolveBlocker rejects), promote the
			// row's kind so the M1 waiver path stays enforced even when an
			// earlier softer condition (user_input, etc.) created the row.
			promotedKind := open.Kind
			if p.Kind == BlockerKindDepFailure {
				promotedKind = BlockerKindDepFailure
			}
			if err := q.MergeAgentTaskBlocker(ctx, sqlc.MergeAgentTaskBlockerParams{
				Detail: merged, Kind: promotedKind, ID: open.ID,
			}); err != nil {
				return err
			}
			return s.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
				TaskID:    nullable(p.TaskID),
				BlockerID: nullable(open.ID),
				EventType: "blocker_merged",
				ActorType: actorTypeOrSystem(p.Actor),
				ActorID:   nullable(p.Actor.ID),
				Detail:    detailJSON(map[string]any{"new_kind": p.Kind, "question": p.Question, "promoted_kind": promotedKind}),
			})
		}

		switch task.Status {
		case StatusReady, StatusRunning:
			// ok
		default:
			return ErrInvalidTransition
		}
		// Stale-worker guard: if the caller is a worker (p.RunID set), the
		// task's active run must still be theirs. Prevents a lease-expired
		// worker from blocking a task that the dispatcher has re-claimed
		// under a new run.
		if p.RunID != "" && (!task.ActiveRunID.Valid || task.ActiveRunID.String != p.RunID) {
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
		// Always clear active_run_id when a block fires — even from from=ready,
		// where a previously-finalised run may still be hanging off the
		// pointer (M2). FinishAgentTaskRun is conditional on still-in-flight
		// status so a finalised run is left untouched; the UPDATE is the
		// guard against overwriting a successful Submit.
		if task.ActiveRunID.Valid {
			if _, err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: RunCancelled, Result: "{}", Error: "blocked",
				FinishedAt: sql.NullString{String: now, Valid: true},
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
			ResolvedAt: sql.NullString{String: now, Valid: true},
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
// downstream's dep_failure blocker (if any) is also resolved — but only when
// no other unwaived hard-failed deps remain (H5).
//
// Authority (H3): the waiver is recorded with actor.ID as waived_by_user; the
// actor must be a user, and must own the task or be an org admin
// (org-admin check is a hook left for the auth layer to refine — for now we
// require actor.Type == ActorUser and actor.ID == task.user_id).
func (s *TransitionService) WaiveDep(ctx context.Context, taskID, depTaskID, reason string, actor Actor) error {
	if reason == "" {
		return fmt.Errorf("WaiveDep: reason is required")
	}
	if actor.Type != ActorUser || actor.ID == "" {
		return ErrUnauthorized
	}
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		// Authority check: task owner only. Org-admin escalation can layer
		// on top once roles are wired.
		if task.UserID != actor.ID {
			return ErrUnauthorized
		}
		now := s.now()
		n, err := q.WaiveAgentTaskDep(ctx, sqlc.WaiveAgentTaskDepParams{
			WaivedAt:     sql.NullString{String: now, Valid: true},
			WaivedByUser: nullable(actor.ID),
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
		// H5: only resolve the dep_failure blocker if no other hard-failed/
		// cancelled-block deps remain unwaived. A merged blocker can cover
		// multiple failed upstreams; waiving one shouldn't unblock the task
		// if another is still outstanding.
		if open, err := q.GetOpenBlockerForTask(ctx, taskID); err == nil && open.Kind == BlockerKindDepFailure {
			deps, err := q.ListAgentTaskDepsWithUpstream(ctx, taskID)
			if err != nil {
				return err
			}
			if !anyUnresolvedDepFailure(deps) {
				if _, err := q.ResolveAgentTaskBlocker(ctx, sqlc.ResolveAgentTaskBlockerParams{
					Resolution: detailJSON(map[string]any{"waived_dep_task_id": depTaskID, "reason": reason}),
					ResolvedAt: sql.NullString{String: now, Valid: true},
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

// anyUnresolvedDepFailure reports whether any dep edge is still blocking the
// task from a dep_failure standpoint: hard kind, upstream failed/cancelled,
// on_failure=block, and not waived.
func anyUnresolvedDepFailure(deps []sqlc.ListAgentTaskDepsWithUpstreamRow) bool {
	for _, r := range deps {
		e := r.AgentTaskDep
		if e.DepKind != DepKindHard || e.OnFailure != OnFailureBlock || e.WaivedAt.Valid {
			continue
		}
		if r.UpstreamStatus == StatusFailed || r.UpstreamStatus == StatusCancelled {
			return true
		}
	}
	return false
}

// FailParams captures a worker-or-system-declared run failure.
type FailParams struct {
	TaskID    string
	RunID     string
	Reason    string // free-form error message
	Retryable bool   // if false, force terminal failure
	Actor     Actor
}

// Fail records a run failure and either returns the task to StatusReady (if
// the retry budget allows and Retryable is true) or moves it to StatusFailed.
func (s *TransitionService) Fail(ctx context.Context, p FailParams) error {
	return s.failTx(ctx, p, RunFailed, "fail", "fail_retry")
}

// InterruptStaleRunParams describes a lease-expired run that the dispatcher
// is finalising on the original worker's behalf.
type InterruptStaleRunParams struct {
	TaskID string
	RunID  string
	Reason string // typically "lease expired"
	Actor  Actor
}

// InterruptStaleRun finalises a run whose lease expired and either returns
// the task to ready (retry budget allowing) or moves it to failed. Distinct
// from Fail so the audit trail records run_status=interrupted +
// event=run_interrupted / run_interrupt_retry, instead of a generic fail
// label that misrepresents the cause (M11).
func (s *TransitionService) InterruptStaleRun(ctx context.Context, p InterruptStaleRunParams) error {
	return s.failTx(ctx,
		FailParams{TaskID: p.TaskID, RunID: p.RunID, Reason: p.Reason, Retryable: true, Actor: p.Actor},
		RunInterrupted, "run_interrupted", "run_interrupt_retry")
}

// failTx is the shared body for Fail and InterruptStaleRun: it parameterises
// the run-row terminal status and the audit event names so callers can be
// honest about whether this was a failure or a lease-expiry interrupt
// without leaking a "RunStatusOnFail" override knob (M11 POSD).
func (s *TransitionService) failTx(ctx context.Context, p FailParams, runStatus, evtTerminal, evtRetry string) error {
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
		// Stale-worker / stale-sweep guard: the task must still be pointing
		// at p.RunID. Without this a worker whose lease expired could call
		// Fail and demote a freshly-claimed run, and the sweep could
		// finalise a run that Submit already cleared.
		if p.RunID != "" && (!task.ActiveRunID.Valid || task.ActiveRunID.String != p.RunID) {
			return ErrInvalidTransition
		}
		now := s.now()
		if p.RunID != "" {
			if _, err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: runStatus, Result: "{}", Error: p.Reason,
				FinishedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:  now, ID: p.RunID,
			}); err != nil {
				return err
			}
		}
		next := StatusReady
		var budgetExhausted bool
		if !p.Retryable || task.RetryCount+1 > task.MaxRetries {
			next = StatusFailed
			budgetExhausted = !p.Retryable || task.RetryCount+1 > task.MaxRetries
		}
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
		if err := q.SetAgentTaskActiveRun(ctx, sqlc.SetAgentTaskActiveRunParams{
			ActiveRunID: sql.NullString{}, UpdatedAt: now, ID: p.TaskID,
		}); err != nil {
			return err
		}
		evt := evtRetry
		if next == StatusFailed {
			evt = evtTerminal
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
		_ = budgetExhausted // preserved for future signalling; not a hard error today
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
			CancelledAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: taskID,
		}); err != nil {
			return err
		}
		// Cancel an in-flight run, clear active pointers. FinishAgentTaskRun
		// is conditional on status IN ('queued','running'), so if Submit
		// already finalised the run before Cancel got here the call is a
		// no-op and the worker's output is preserved (H7).
		if task.ActiveRunID.Valid {
			if _, err := q.FinishAgentTaskRun(ctx, sqlc.FinishAgentTaskRunParams{
				Status: RunCancelled, Result: "{}", Error: reason,
				FinishedAt: sql.NullString{String: now, Valid: true},
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
				ResolvedAt: sql.NullString{String: now, Valid: true}, ID: task.ActiveBlockerID.String,
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
// default to "hard" / "block" when blank. Cross-org edges are rejected:
// readiness joins on agent_task globally and on_failure propagation would
// otherwise let an Org A task gate (or be gated by) an Org B task.
func (s *TransitionService) AddDep(ctx context.Context, taskID, depTaskID, depKind, onFailure string) error {
	if taskID == "" || depTaskID == "" {
		return fmt.Errorf("AddDep: task and dep ids required")
	}
	if taskID == depTaskID {
		return ErrCycle
	}
	if depKind == "" {
		depKind = DepKindHard
	}
	if onFailure == "" {
		onFailure = OnFailureBlock
	}
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		return s.addDepTx(ctx, q, taskID, depTaskID, depKind, onFailure)
	})
}

// addDepTx is the inner AddDep body, callable from inside an existing tx
// (CreateTask uses this to keep dep inserts in the same transaction as the
// task row — B1).
func (s *TransitionService) addDepTx(ctx context.Context, q *sqlc.Queries, taskID, depTaskID, depKind, onFailure string) error {
	// Cross-org check (H1): both endpoints must share an org_id.
	task, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		return err
	}
	dep, err := q.GetAgentTask(ctx, depTaskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		return err
	}
	if task.OrgID != dep.OrgID {
		return ErrCrossOrg
	}
	if reaches, err := reachable(ctx, q, depTaskID, taskID); err != nil {
		return err
	} else if reaches {
		return ErrCycle
	}
	_, err = q.CreateAgentTaskDep(ctx, sqlc.CreateAgentTaskDepParams{
		TaskID:    taskID,
		DepTaskID: depTaskID,
		DepKind:   depKind,
		OnFailure: onFailure,
		CreatedAt: s.now(),
	})
	return err
}

// reachable reports whether `from` can reach `target` by following dep edges
// (from -> dep_task_id -> dep_task_id -> ...). The walk is bounded by the
// node-visit budget to defend against pathological graphs. If the budget is
// exhausted without an answer, the function returns
// ErrCycleCheckBudgetExceeded so the caller can fail closed instead of
// accepting an edge that may close a cycle (M4).
func reachable(ctx context.Context, q *sqlc.Queries, from, target string) (bool, error) {
	stack := []string{from}
	seen := map[string]struct{}{from: {}}
	const maxVisits = 10000
	visits := 0
	for len(stack) > 0 {
		if visits >= maxVisits {
			return false, ErrCycleCheckBudgetExceeded
		}
		visits++
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
