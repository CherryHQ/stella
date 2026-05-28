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
	svc *TransitionService
	q   *sqlc.Queries
	db  *sql.DB
}

// NewServiceFacade builds the facade.
func NewServiceFacade(db *sql.DB, q *sqlc.Queries, svc *TransitionService) *ServiceFacade {
	return &ServiceFacade{svc: svc, q: q, db: db}
}

// CreateTaskInput is the request body for CreateTask. Fields are optional
// except where noted.
type CreateTaskInput struct {
	OrgID            string // required
	UserID           string // required
	Title            string // required
	Description      string
	Priority         string // routine | urgent; "" => routine
	AgentID          string // creator agent (D12); optional
	ExecutorAgentID  string // explicit override (D13); optional, written as dispatch hint
	Required         bool   // defaults to true if zero-value at insert
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
	if in.OrgID == "" || in.UserID == "" || in.Title == "" {
		return sqlc.AgentTask{}, fmt.Errorf("CreateTask: org_id, user_id, and title are required")
	}
	priority := in.Priority
	if priority == "" {
		priority = "routine"
	}
	if priority != "routine" && priority != "urgent" {
		return sqlc.AgentTask{}, fmt.Errorf("CreateTask: invalid priority %q", priority)
	}
	if in.MaxRetries <= 0 {
		in.MaxRetries = 3
	}
	if in.Context == "" {
		in.Context = "{}"
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	required := int64(1)
	if !in.Required {
		// Default behavior: required=true when caller omits. To create a
		// non-required task explicitly pass Required=true; the field's name
		// reads naturally but the legacy default is preserved.
		required = 1
	}

	var task sqlc.AgentTask
	err := f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		var err error
		task, err = q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
			ID:          id,
			OrgID:       in.OrgID,
			UserID:      in.UserID,
			AgentID:     nullable(in.AgentID),
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

// GetTask returns a task by id within an org.
func (f *ServiceFacade) GetTask(ctx context.Context, taskID, orgID string) (sqlc.AgentTask, error) {
	t, err := f.q.GetAgentTaskForOrg(ctx, sqlc.GetAgentTaskForOrgParams{ID: taskID, OrgID: orgID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.AgentTask{}, ErrTaskNotFound
		}
		return sqlc.AgentTask{}, err
	}
	return t, nil
}

// ListTasksByOrg returns paginated tasks for an org.
func (f *ServiceFacade) ListTasksByOrg(ctx context.Context, orgID string, limit, offset int64) ([]sqlc.AgentTask, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentTasksByOrg(ctx, sqlc.ListAgentTasksByOrgParams{
		OrgID: orgID, Limit: limit, Offset: offset,
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

// ListEvents returns the full audit trail for a task.
func (f *ServiceFacade) ListEvents(ctx context.Context, taskID string) ([]sqlc.AgentTaskEvent, error) {
	return f.q.ListAgentTaskEvents(ctx, nullable(taskID))
}

// ListRuns returns all runs for a task, newest attempt first.
func (f *ServiceFacade) ListRuns(ctx context.Context, taskID string) ([]sqlc.AgentTaskRun, error) {
	return f.q.ListAgentTaskRunsByTask(ctx, nullable(taskID))
}

// ListDeps returns the dep edges (with upstream status) for a task.
func (f *ServiceFacade) ListDeps(ctx context.Context, taskID string) ([]sqlc.ListAgentTaskDepsWithUpstreamRow, error) {
	return f.q.ListAgentTaskDepsWithUpstream(ctx, taskID)
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

func timeOrNull(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}
