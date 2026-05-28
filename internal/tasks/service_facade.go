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
	Optional         bool   // M8: zero value (false) means required; set true to opt out
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
// writes a dispatch hint, and adds dependency edges with cycle checking. All
// of it runs in a single transaction (H6 / B1): a partial failure rolls back
// the task, deps, and hint together, so callers never see an orphan task
// missing its requested executor or half its deps.
//
// Cross-org checks (H2): agent_id and executor_agent_id, if set, must both
// belong to in.OrgID — otherwise a creator in Org A could pin their task to
// an Org B agent and exfiltrate its prompt/config at dispatch time. Dep
// endpoints are likewise org-checked inside TransitionService.AddDep (H1).
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
	// M8: schema default for required is 1; Optional=true flips it to 0.
	required := int64(1)
	if in.Optional {
		required = 0
	}
	var out sqlc.AgentTask
	err := f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		// H2: both agent fields must belong to the task's org. GetAgent's
		// WHERE clause is `id = ? AND org_id = ?`, so a wrong-org id surfaces
		// as ErrNoRows.
		if in.AgentID != "" {
			if _, err := q.GetAgent(ctx, sqlc.GetAgentParams{ID: in.AgentID, OrgID: in.OrgID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("CreateTask: agent_id: %w", ErrCrossOrg)
				}
				return fmt.Errorf("CreateTask: lookup agent: %w", err)
			}
		}
		if in.ExecutorAgentID != "" {
			if _, err := q.GetAgent(ctx, sqlc.GetAgentParams{ID: in.ExecutorAgentID, OrgID: in.OrgID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("CreateTask: executor_agent_id: %w", ErrCrossOrg)
				}
				return fmt.Errorf("CreateTask: lookup executor agent: %w", err)
			}
		}
		task, err := q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
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
			return fmt.Errorf("CreateTask: %w", err)
		}
		// Deps inside the same tx via the addDepTx helper (cross-org + cycle
		// checks included).
		for _, d := range in.Deps {
			kind := d.Kind
			if kind == "" {
				kind = DepKindHard
			}
			of := d.OnFailure
			if of == "" {
				of = OnFailureBlock
			}
			if id == d.DepTaskID {
				return ErrCycle
			}
			if err := f.svc.addDepTx(ctx, q, id, d.DepTaskID, kind, of); err != nil {
				return fmt.Errorf("CreateTask: dep %s: %w", d.DepTaskID, err)
			}
		}
		if in.ExecutorAgentID != "" {
			if _, err := q.CreateAgentTaskDispatchHint(ctx, sqlc.CreateAgentTaskDispatchHintParams{
				ID:              uuid.NewString(),
				TaskID:          id,
				Kind:            RunKindWorker,
				ExecutorAgentID: in.ExecutorAgentID,
				CreatedAt:       now,
			}); err != nil {
				return fmt.Errorf("CreateTask: dispatch hint: %w", err)
			}
		}
		if in.ActivateOnCreate {
			if err := f.svc.activateTx(ctx, q, id, Actor{Type: ActorUser, ID: in.UserID}); err != nil {
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

// requireOrgTask asserts the task exists and belongs to orgID. Used by every
// org-scoped facade method to enforce tenant isolation (M5).
func (f *ServiceFacade) requireOrgTask(ctx context.Context, taskID, orgID string) (sqlc.AgentTask, error) {
	t, err := f.q.GetAgentTaskForOrg(ctx, sqlc.GetAgentTaskForOrgParams{ID: taskID, OrgID: orgID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.AgentTask{}, ErrTaskNotFound
		}
		return sqlc.AgentTask{}, err
	}
	return t, nil
}

// GetReadiness loads the task + its deps and returns the computed view.
// Org-scoped (M5).
func (f *ServiceFacade) GetReadiness(ctx context.Context, taskID, orgID string) (Readiness, error) {
	t, err := f.requireOrgTask(ctx, taskID, orgID)
	if err != nil {
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

// ListEvents returns the full audit trail for a task. Org-scoped (M5).
func (f *ServiceFacade) ListEvents(ctx context.Context, taskID, orgID string) ([]sqlc.AgentTaskEvent, error) {
	if _, err := f.requireOrgTask(ctx, taskID, orgID); err != nil {
		return nil, err
	}
	return f.q.ListAgentTaskEvents(ctx, nullable(taskID))
}

// ListRuns returns all runs for a task, newest attempt first. Org-scoped (M5).
func (f *ServiceFacade) ListRuns(ctx context.Context, taskID, orgID string) ([]sqlc.AgentTaskRun, error) {
	if _, err := f.requireOrgTask(ctx, taskID, orgID); err != nil {
		return nil, err
	}
	return f.q.ListAgentTaskRunsByTask(ctx, nullable(taskID))
}

// ListDeps returns the dep edges (with upstream status) for a task.
// Org-scoped (M5).
func (f *ServiceFacade) ListDeps(ctx context.Context, taskID, orgID string) ([]sqlc.ListAgentTaskDepsWithUpstreamRow, error) {
	if _, err := f.requireOrgTask(ctx, taskID, orgID); err != nil {
		return nil, err
	}
	return f.q.ListAgentTaskDepsWithUpstream(ctx, taskID)
}

// AddDep is a thin shim over TransitionService.AddDep. Org-scoped on the
// owning task (M5); the cross-org check on the edge itself lives in AddDep.
func (f *ServiceFacade) AddDep(ctx context.Context, taskID, depTaskID, kind, onFailure, orgID string) error {
	if _, err := f.requireOrgTask(ctx, taskID, orgID); err != nil {
		return err
	}
	return f.svc.AddDep(ctx, taskID, depTaskID, kind, onFailure)
}

// CancelTask exposes TransitionService.Cancel. Org-scoped (M5).
func (f *ServiceFacade) CancelTask(ctx context.Context, taskID, orgID, reason string, actor Actor) error {
	if _, err := f.requireOrgTask(ctx, taskID, orgID); err != nil {
		return err
	}
	return f.svc.Cancel(ctx, taskID, reason, actor)
}

// ResolveBlocker exposes TransitionService.ResolveBlocker. dep_failure
// blockers return ErrDepFailureUnresolved per D14 / M1. Org-scoped via the
// blocker's owning task (M5).
func (f *ServiceFacade) ResolveBlocker(ctx context.Context, blockerID, orgID, resolution string, actor Actor) error {
	bl, err := f.q.GetAgentTaskBlocker(ctx, blockerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBlockerNotFound
		}
		return err
	}
	if _, err := f.requireOrgTask(ctx, bl.TaskID, orgID); err != nil {
		return err
	}
	return f.svc.ResolveBlocker(ctx, blockerID, resolution, actor)
}

// WaiveDep exposes TransitionService.WaiveDep (D14 / M1 — required for
// dep_failure blockers). The waiver is attributed to actor.ID; the auth
// layer must populate Actor from the authenticated principal (H3).
// Org-scoped (M5).
func (f *ServiceFacade) WaiveDep(ctx context.Context, taskID, depTaskID, orgID, reason string, actor Actor) error {
	if _, err := f.requireOrgTask(ctx, taskID, orgID); err != nil {
		return err
	}
	return f.svc.WaiveDep(ctx, taskID, depTaskID, reason, actor)
}

func timeOrNull(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}
