package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ServiceFacade is the v2 Go-level surface that HTTP handlers + CLI bind to.
// Phase 5 deliverable. The OpenAPI rewrite (flat /api/tasks routes, regen of
// types/server/client) is deliberately deferred — see plan.md Phase 5
// handoff. The current admin HTTP handlers continue to return 503 (MP1 stub)
// while this facade is exercised programmatically (boot wiring + tests).
//
// All mutating methods route through TransitionService so the
// status-write-only-via-transition invariant holds.
type ServiceFacade struct {
	svc        *TransitionService
	q          *sqlc.Queries
	db         *sql.DB
	newSession SessionMinter
}

// ErrInvalidTaskContext marks invalid task/goal ownership context at the write boundary.
var ErrInvalidTaskContext = errors.New("tasks: invalid task context")

// NewServiceFacade builds the facade.
func NewServiceFacade(db *sql.DB, q *sqlc.Queries, svc *TransitionService, newSession SessionMinter) *ServiceFacade {
	return &ServiceFacade{svc: svc, q: q, db: db, newSession: newSession}
}

// CreateTaskInput is the request body for CreateTask. Fields are optional
// except where noted.
type CreateTaskInput struct {
	UserID           string // required
	Title            string // required
	Description      string
	Priority         string // routine | urgent; "" => routine
	AgentID          string // owner/manager agent context; required unless inherited from goal
	GoalID           string // parent goal; optional
	ProjectID        string // project/workspace context; optional
	ExecutorAgentID  string // explicit override (D13); optional, written as dispatch hint
	Required         *bool  // nil => true (default); explicit false => optional task
	MaxRetries       int64
	NotBefore        time.Time // zero => null
	DeadlineAt       time.Time // zero => null
	Context          string    // JSON; "" => "{}"
	Deps             []DepInput
	ActivateOnCreate bool // if true, transition from draft to ready before returning
}

// DepInput captures one outgoing edge to be created with the task.
type DepInput struct {
	DepTaskID string
	Kind      string // hard|soft; "" => hard
	OnFailure string // block|fail|ignore; "" => block
}

// CreateTask creates a task in 'draft', optionally activates it, optionally
// writes a dispatch hint, and adds dependency edges with cycle checking.
func (f *ServiceFacade) CreateTask(ctx context.Context, in CreateTaskInput) (sqlc.AgentTask, error) {
	if in.UserID == "" || in.Title == "" {
		return sqlc.AgentTask{}, fmt.Errorf("CreateTask: user_id and title are required")
	}
	// D5 backdoor shut (#525): goal work tasks come only from a materialized
	// plan, never hand-attached. A goal_id here would let a caller seed children
	// outside the gate, so reject it and point at the plan path.
	if in.GoalID != "" {
		return sqlc.AgentTask{}, ErrPlanMaterializationRequired
	}
	priority := in.Priority
	if priority == "" {
		priority = PriorityRoutine
	}
	if !validPriority(priority) {
		return sqlc.AgentTask{}, fmt.Errorf("CreateTask: invalid priority %q", priority)
	}
	if in.MaxRetries <= 0 {
		in.MaxRetries = 3
	}
	if in.Context == "" {
		in.Context = "{}"
	}
	resolvedAgentID, resolvedProjectID, err := f.resolveTaskContext(ctx, in)
	if err != nil {
		return sqlc.AgentTask{}, err
	}
	if f.newSession == nil {
		return sqlc.AgentTask{}, fmt.Errorf("%w: task session minter is not configured", ErrInvalidTaskContext)
	}
	sessionID, err := f.newSession(ctx, in.UserID, resolvedAgentID, resolvedProjectID)
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("mint task session: %w", err)
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	required := int64(1)
	if in.Required != nil && !*in.Required {
		required = 0
	}

	var task sqlc.AgentTask
	err = f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		var err error
		task, err = q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
			ID:          id,
			UserID:      in.UserID,
			AgentID:     resolvedAgentID,
			SessionID:   sessionID,
			GoalID:      nullable(in.GoalID),
			ProjectID:   nullable(resolvedProjectID),
			Title:       in.Title,
			Description: in.Description,
			Status:      StatusDraft,
			Priority:    priority,
			Required:    required,
			RetryCount:  0,
			MaxRetries:  in.MaxRetries,
			NotBefore:   timeOrNull(in.NotBefore),
			DeadlineAt:  timeOrNull(in.DeadlineAt),
			Context:     in.Context,
			Output:      "{}",
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		if err != nil {
			return err
		}
		for _, d := range in.Deps {
			if err := f.svc.AddDepInTx(ctx, q, id, d.DepTaskID, d.Kind, d.OnFailure); err != nil {
				return fmt.Errorf("dep %s: %w", d.DepTaskID, err)
			}
		}
		if in.ExecutorAgentID != "" {
			if _, err := q.CreateAgentTaskDispatchHint(ctx, sqlc.CreateAgentTaskDispatchHintParams{
				ID:              uuid.NewString(),
				TaskID:          nullable(id),
				Kind:            RunKindWorker,
				ExecutorAgentID: in.ExecutorAgentID,
				CreatedAt:       now,
			}); err != nil {
				return fmt.Errorf("dispatch hint: %w", err)
			}
		}
		if in.ActivateOnCreate {
			if err := f.svc.ActivateInTx(ctx, q, id, Actor{Type: ActorUser, ID: in.UserID}); err != nil {
				return fmt.Errorf("activate: %w", err)
			}
			task.Status = StatusReady
		}
		return nil
	})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("CreateTask: %w", err)
	}
	return task, nil
}

func (f *ServiceFacade) resolveTaskContext(ctx context.Context, in CreateTaskInput) (string, string, error) {
	agentID := in.AgentID
	projectID := in.ProjectID
	if in.GoalID != "" {
		goal, err := f.q.GetAgentGoal(ctx, in.GoalID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", ErrGoalNotFound
			}
			return "", "", err
		}
		if goal.UserID != in.UserID {
			return "", "", ErrGoalNotFound
		}
		// An archived goal is inert: it must not accept new child tasks, or a
		// task created under it would be dispatchable while the goal stays
		// hidden — reviving archived work. Unarchive the goal first (CR-017).
		if goal.ArchivedAt.Valid {
			return "", "", fmt.Errorf("%w: goal is archived and accepts no new tasks", ErrInvalidTaskContext)
		}
		// A failed goal is recoverable by rollup, but only by reopening or
		// completing its existing children — not by attaching new work. Keep it
		// terminal for task creation so a goal cannot sit failed while accepting
		// fresh tasks; reopen a failed child (which recovers the goal to running)
		// before adding more. See isTerminalGoalStatus vs isQuiescentGoalStatus.
		if isTerminalGoalStatus(goal.Status) {
			return "", "", fmt.Errorf("%w: goal is %s and accepts no new tasks", ErrInvalidTaskContext, goal.Status)
		}
		if goal.AgentID == "" {
			return "", "", fmt.Errorf("%w: goal has no agent_id", ErrInvalidTaskContext)
		}
		if agentID == "" {
			agentID = goal.AgentID
		} else if agentID != goal.AgentID {
			return "", "", fmt.Errorf("%w: goal_id must belong to the same agent_id", ErrInvalidTaskContext)
		}
		if goal.ProjectID.Valid {
			if projectID == "" {
				projectID = goal.ProjectID.String
			} else if projectID != goal.ProjectID.String {
				return "", "", fmt.Errorf("%w: goal_id must belong to the same project_id", ErrInvalidTaskContext)
			}
		}
	}
	if agentID == "" {
		return "", "", fmt.Errorf("%w: agent_id is required", ErrInvalidTaskContext)
	}
	if projectID != "" {
		project, err := f.q.GetProject(ctx, sqlc.GetProjectParams{ID: projectID, UserID: in.UserID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", fmt.Errorf("%w: project_id not found", ErrInvalidTaskContext)
			}
			return "", "", err
		}
		if project.AgentID != agentID {
			return "", "", fmt.Errorf("%w: project_id must belong to the same agent_id", ErrInvalidTaskContext)
		}
	}
	return agentID, projectID, nil
}

// GetTask returns a task by id.
func (f *ServiceFacade) GetTask(ctx context.Context, taskID string) (sqlc.AgentTask, error) {
	t, err := f.q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.AgentTask{}, ErrTaskNotFound
		}
		return sqlc.AgentTask{}, err
	}
	return t, nil
}

// TaskFilter narrows a task list. Named fields avoid the transposition footgun
// of several adjacent string parameters (mirrors GoalFilter); the zero value
// lists active tasks.
type TaskFilter struct {
	AgentID   string
	ProjectID string
	Status    string
	Archived  bool // true lists the archived (history/restore) set instead of active tasks
}

// ListTasksByUser returns paginated tasks owned by the given user, narrowed by
// filter. The zero filter lists active tasks; Archived=true lists the archived
// history/restore set. Empty filter strings match all rows.
func (f *ServiceFacade) ListTasksByUser(ctx context.Context, userID string, filter TaskFilter, limit, offset int64) ([]sqlc.AgentTask, error) {
	if limit <= 0 {
		limit = 50
	}
	var arch any
	if filter.Archived {
		arch = int64(1)
	}
	return f.q.ListAgentTasksByUser(ctx, sqlc.ListAgentTasksByUserParams{
		UserID:    userID,
		Archived:  arch,
		AgentID:   nilIfEmpty(filter.AgentID),
		ProjectID: nilIfEmpty(filter.ProjectID),
		Status:    nilIfEmpty(filter.Status),
		Limit:     limit,
		Offset:    offset,
	})
}

// GetReadiness loads the task + its deps and returns the computed view.
func (f *ServiceFacade) GetReadiness(ctx context.Context, taskID string) (Readiness, error) {
	t, err := f.q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Readiness{}, ErrTaskNotFound
		}
		return Readiness{}, err
	}
	rows, err := f.q.ListAgentTaskDepsWithUpstream(ctx, taskID)
	if err != nil {
		return Readiness{}, err
	}
	views := make([]DepEdgeView, 0, len(rows))
	for _, r := range rows {
		views = append(views, DepEdgeView{
			DepTaskID:      r.AgentTaskDep.DepTaskID,
			Kind:           r.AgentTaskDep.DepKind,
			OnFailure:      r.AgentTaskDep.OnFailure,
			Waived:         r.AgentTaskDep.WaivedAt.Valid,
			UpstreamStatus: r.UpstreamStatus,
		})
	}
	return Compute(t, views, time.Now().UTC()), nil
}

// ListEvents returns the audit trail for a task, oldest first.
func (f *ServiceFacade) ListEvents(ctx context.Context, taskID string, limit, offset int64) ([]sqlc.AgentTaskEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentTaskEvents(ctx, sqlc.ListAgentTaskEventsParams{
		TaskID: nullable(taskID),
		Limit:  limit,
		Offset: offset,
	})
}

// ListRuns returns runs for a task, newest attempt first.
func (f *ServiceFacade) ListRuns(ctx context.Context, taskID string, limit, offset int64) ([]sqlc.AgentTaskRun, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentTaskRunsByTask(ctx, sqlc.ListAgentTaskRunsByTaskParams{
		TaskID: nullable(taskID),
		Limit:  limit,
		Offset: offset,
	})
}

// ListDeps returns the dep edges (with upstream status) for a task.
func (f *ServiceFacade) ListDeps(ctx context.Context, taskID string, limit, offset int64) ([]sqlc.ListAgentTaskDepsWithUpstreamPagedRow, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentTaskDepsWithUpstreamPaged(ctx, sqlc.ListAgentTaskDepsWithUpstreamPagedParams{
		TaskID: taskID,
		Limit:  limit,
		Offset: offset,
	})
}

// AddDep is a thin shim over TransitionService.AddDep for caller ergonomics.
func (f *ServiceFacade) AddDep(ctx context.Context, taskID, depTaskID, kind, onFailure string) error {
	return f.svc.AddDep(ctx, taskID, depTaskID, kind, onFailure)
}

// CancelTask exposes TransitionService.Cancel.
func (f *ServiceFacade) CancelTask(ctx context.Context, taskID, reason string, actor Actor) error {
	return f.svc.Cancel(ctx, taskID, reason, actor)
}

// ResolveBlocker exposes TransitionService.ResolveBlocker. dep_failure
// blockers return ErrDepFailureUnresolved per D14 / M1.
func (f *ServiceFacade) ResolveBlocker(ctx context.Context, blockerID, resolution string, actor Actor) error {
	return f.svc.ResolveBlocker(ctx, blockerID, resolution, actor)
}

// WaiveDep exposes TransitionService.WaiveDep (D14 / M1 — required for
// dep_failure blockers).
func (f *ServiceFacade) WaiveDep(ctx context.Context, taskID, depTaskID, userID, reason string, actor Actor) error {
	return f.svc.WaiveDep(ctx, taskID, depTaskID, userID, reason, actor)
}

// ArchiveTask hides a terminal/draft task from default lists while preserving audit data.
// Re-archiving an already-archived task is a no-op (idempotent), matching HTTP DELETE semantics.
func (f *ServiceFacade) ArchiveTask(ctx context.Context, taskID string, actor Actor) error {
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		t, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		if t.ArchivedAt.Valid {
			return nil
		}
		// Individually archiving a goal child while its parent goal is still live
		// would strand the goal: a draft child stays counted as required_pending
		// in the rollup (GoalChildCounts ignores archived_at), so hiding it wedges
		// the goal in 'running' with an invisible blocker. Children are cleared by
		// archiving the whole goal (which cascades) once it is terminal/archived.
		if t.GoalID.Valid {
			goal, err := q.GetAgentGoal(ctx, t.GoalID.String)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && !goal.ArchivedAt.Valid && !isTerminalGoalStatus(goal.Status) {
				return ErrInvalidTransition
			}
		}
		_, err = f.archiveTaskTx(ctx, q, taskID, "", `{"mode":"archive"}`, actor)
		return err
	})
}

// UnarchiveTask restores a standalone archived task to default lists, reversing
// ArchiveTask. Restoring a non-archived task is a no-op (idempotent).
func (f *ServiceFacade) UnarchiveTask(ctx context.Context, taskID string, actor Actor) error {
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		t, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return err
		}
		if !t.ArchivedAt.Valid {
			return nil
		}
		// Restoring a child while its goal is still archived would resurrect the
		// task under a hidden parent — an inert goal must stay fully inert. Make
		// the user unarchive the goal first, which cascades the child back
		// (CR-018).
		if t.GoalID.Valid {
			goal, err := q.GetAgentGoal(ctx, t.GoalID.String)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && goal.ArchivedAt.Valid {
				return ErrInvalidTransition
			}
		}
		_, err = f.unarchiveTaskTx(ctx, q, taskID, t.GoalID.String, `{"mode":"unarchive"}`, actor)
		return err
	})
}

// archiveTaskTx archives one task inside an open tx. The status fetch and check
// happen in-tx so a concurrent transition (e.g. draft→ready) that committed
// before this write is respected instead of being silently overwritten. Returns
// whether a row was archived; an already-archived task is a no-op (false, nil),
// while a non-archivable status aborts with ErrInvalidTransition (the caller's
// signal to abort an enclosing goal archive).
func (f *ServiceFacade) archiveTaskTx(ctx context.Context, q *sqlc.Queries, taskID, goalID, detail string, actor Actor) (bool, error) {
	t, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrTaskNotFound
		}
		return false, err
	}
	if t.ArchivedAt.Valid {
		return false, nil
	}
	if !isArchivableTaskStatus(t.Status) {
		return false, ErrInvalidTransition
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	n, err := q.ArchiveAgentTask(ctx, sqlc.ArchiveAgentTaskParams{ArchivedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: taskID})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	return true, f.svc.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		TaskID:     nullable(taskID),
		GoalID:     nullable(goalID),
		EventType:  "task_archive",
		FromStatus: nullable(t.Status),
		ToStatus:   nullable(t.Status),
		ActorType:  actor.Type,
		ActorID:    nullable(actor.ID),
		Detail:     detail,
	})
}

// unarchiveTaskTx clears archived_at on one task inside an open tx and records a
// task_unarchive event. Returns whether a row changed; an already-active task is
// a no-op (false, nil). Mirrors archiveTaskTx.
func (f *ServiceFacade) unarchiveTaskTx(ctx context.Context, q *sqlc.Queries, taskID, goalID, detail string, actor Actor) (bool, error) {
	t, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrTaskNotFound
		}
		return false, err
	}
	if !t.ArchivedAt.Valid {
		return false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	n, err := q.UnarchiveAgentTask(ctx, sqlc.UnarchiveAgentTaskParams{UpdatedAt: now, ID: taskID})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	return true, f.svc.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
		TaskID:     nullable(taskID),
		GoalID:     nullable(goalID),
		EventType:  "task_unarchive",
		FromStatus: nullable(t.Status),
		ToStatus:   nullable(t.Status),
		ActorType:  actor.Type,
		ActorID:    nullable(actor.ID),
		Detail:     detail,
	})
}

// ReopenTask exposes TransitionService.ReopenTask. Cascade applies the
// downstream reset rules in D10.
func (f *ServiceFacade) ReopenTask(ctx context.Context, taskID string, cascade bool, actor Actor) error {
	return f.svc.ReopenTask(ctx, taskID, cascade, actor)
}

// ListReviews returns the review history for a task, newest first.
func (f *ServiceFacade) ListReviews(ctx context.Context, taskID string, limit, offset int64) ([]sqlc.AgentReview, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentReviewsByTask(ctx, sqlc.ListAgentReviewsByTaskParams{
		TaskID: nullable(taskID),
		Limit:  limit,
		Offset: offset,
	})
}

// ApproveReview exposes TransitionService.ApproveReview.
func (f *ServiceFacade) ApproveReview(ctx context.Context, reviewID, summary string, actor Actor) error {
	return f.svc.ApproveReview(ctx, reviewID, summary, actor)
}

// RejectReview exposes TransitionService.RejectReview.
func (f *ServiceFacade) RejectReview(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return f.svc.RejectReview(ctx, reviewID, summary, feedback, actor)
}

// RequestChanges exposes TransitionService.RequestChanges.
func (f *ServiceFacade) RequestChanges(ctx context.Context, reviewID, summary, feedback string, actor Actor) error {
	return f.svc.RequestChanges(ctx, reviewID, summary, feedback, actor)
}

// EscalateReview exposes TransitionService.EscalateReview.
func (f *ServiceFacade) EscalateReview(ctx context.Context, reviewID, reason string, actor Actor) error {
	return f.svc.EscalateReview(ctx, reviewID, reason, actor)
}

// ListBlockerForTask returns the open blocker (if any) for a task. Phase-2
// handlers use this to translate {blockerID} path params into the underlying
// blocker row before resolving.
func (f *ServiceFacade) GetBlocker(ctx context.Context, blockerID string) (sqlc.AgentTaskBlocker, error) {
	return f.q.GetAgentTaskBlocker(ctx, blockerID)
}

// GetReview returns a review by id. Phase-2 handlers use this to enforce
// task<->review parentage before applying a decision.
func (f *ServiceFacade) GetReview(ctx context.Context, reviewID string) (sqlc.AgentReview, error) {
	return f.q.GetAgentReview(ctx, reviewID)
}

func timeOrNull(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}

// nilIfEmpty returns nil for an empty string so a sqlc.narg filter matches all
// rows; otherwise it returns the value to filter on.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
