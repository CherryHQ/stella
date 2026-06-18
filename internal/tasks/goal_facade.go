package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ---------------------------------------------------------------------------
// Goal facade
// ---------------------------------------------------------------------------

// CreateGoalInput is the request body for CreateGoal. UserID/Title are
// required; everything else carries safe defaults.
type CreateGoalInput struct {
	UserID       string
	AgentID      string
	ProjectID    string
	Title        string
	Description  string
	Priority     string
	ReviewPolicy string
	Context      string
	PlanMode     string // direct | deferred; "" => direct
}

// normalize applies defaults in place and validates the resolved fields,
// leaving CreateGoal's body to the durable orchestration. ProjectID ownership
// is checked separately because it needs the store.
func (in *CreateGoalInput) normalize() error {
	if in.UserID == "" || in.Title == "" {
		return fmt.Errorf("CreateGoal: user_id and title are required")
	}
	if in.AgentID == "" {
		return fmt.Errorf("%w: agent_id is required", ErrInvalidTaskContext)
	}
	if in.Priority == "" {
		in.Priority = PriorityRoutine
	}
	if !validPriority(in.Priority) {
		return fmt.Errorf("CreateGoal: invalid priority %q", in.Priority)
	}
	if in.ReviewPolicy == "" {
		in.ReviewPolicy = ReviewPolicyNone
	}
	if !validReviewPolicy(in.ReviewPolicy) {
		return fmt.Errorf("CreateGoal: invalid review_policy %q", in.ReviewPolicy)
	}
	// Goal-level review (auto/agent/human) needs the synthesizer/goal-review
	// runtime, which is not wired in this build. Only 'none' is supported.
	if in.ReviewPolicy != ReviewPolicyNone {
		return fmt.Errorf("%w: goal review_policy %q (only 'none' is supported)", ErrUnsupportedReviewPolicy, in.ReviewPolicy)
	}
	if in.Context == "" {
		in.Context = "{}"
	}
	if in.PlanMode == "" {
		in.PlanMode = PlanModeDirect
	}
	if in.PlanMode != PlanModeDirect && in.PlanMode != PlanModeDeferred {
		return fmt.Errorf("%w: %q (use 'direct' or 'deferred')", ErrInvalidPlanMode, in.PlanMode)
	}
	return nil
}

// CreateGoal inserts an agent_goal row, then seeds its plan per PlanMode (#525).
// "direct" (the default) auto-creates+accepts+materializes a one-task direct
// plan and leaves the goal in 'planned', ready to activate. "deferred" leaves
// the goal in 'draft' with no plan row, for a caller that will plan explicitly
// via CreateGoalPlan before activating. Work tasks are never hand-attached: they
// come only from a materialized plan (the CreateTask goal_id backdoor is shut).
func (f *ServiceFacade) CreateGoal(ctx context.Context, in CreateGoalInput) (sqlc.AgentGoal, error) {
	if err := in.normalize(); err != nil {
		return sqlc.AgentGoal{}, err
	}
	if in.ProjectID != "" {
		project, err := f.q.GetProject(ctx, sqlc.GetProjectParams{ID: in.ProjectID, UserID: in.UserID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sqlc.AgentGoal{}, fmt.Errorf("%w: project_id not found", ErrInvalidTaskContext)
			}
			return sqlc.AgentGoal{}, err
		}
		if project.AgentID != in.AgentID {
			return sqlc.AgentGoal{}, fmt.Errorf("%w: project_id must belong to the same agent_id", ErrInvalidTaskContext)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	goalParams := sqlc.CreateAgentGoalParams{
		ID:           uuid.NewString(),
		UserID:       in.UserID,
		AgentID:      in.AgentID,
		ProjectID:    nullable(in.ProjectID),
		Title:        in.Title,
		Description:  in.Description,
		Status:       GoalStatusDraft,
		Priority:     in.Priority,
		ReviewPolicy: in.ReviewPolicy,
		Context:      in.Context,
		Output:       "{}",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if in.PlanMode == PlanModeDeferred {
		return f.q.CreateAgentGoal(ctx, goalParams)
	}
	// Direct: insert goal + plan + task in one tx, so a failed materialize never
	// leaves a draft/no-plan ghost goal (codex SF). The session is pre-minted
	// outside the tx (SQLite single-writer). The goal lands in 'planned' with one
	// ready-on-activate child — never a child-less running window.
	previewGoal := sqlc.AgentGoal{
		ID: goalParams.ID, UserID: in.UserID, AgentID: in.AgentID,
		ProjectID: goalParams.ProjectID, Title: in.Title, Description: in.Description,
		Priority: in.Priority,
	}
	raw, err := buildDirectPlanContent(previewGoal)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	sessions, err := f.mintDirectPlanSession(ctx, previewGoal)
	if err != nil {
		return sqlc.AgentGoal{}, err
	}
	var goal sqlc.AgentGoal
	err = f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		var err error
		goal, err = q.CreateAgentGoal(ctx, goalParams)
		if err != nil {
			return err
		}
		return f.createAndAcceptDirectPlanInTx(ctx, q, goal, raw, sessions, now)
	})
	if err != nil {
		return sqlc.AgentGoal{}, fmt.Errorf("CreateGoal: %w", err)
	}
	return f.GetGoal(ctx, goal.ID)
}

// GetGoal returns one goal by ID.
func (f *ServiceFacade) GetGoal(ctx context.Context, goalID string) (sqlc.AgentGoal, error) {
	g, err := f.q.GetAgentGoal(ctx, goalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.AgentGoal{}, ErrGoalNotFound
		}
		return sqlc.AgentGoal{}, err
	}
	return g, nil
}

// GoalFilter narrows a goal list. Named fields avoid the transposition footgun
// of several adjacent string parameters; the zero value lists active goals.
type GoalFilter struct {
	AgentID   string
	ProjectID string
	Status    string
	Archived  bool   // true lists the archived (history/restore) set instead of active goals
	Terminal  *bool  // nil any; false non-terminal (active); true terminal (history). Ignored when Archived.
	Search    string // case-insensitive substring on title/description; empty matches all
}

func (filter GoalFilter) params(userID string, limit, offset int64) sqlc.ListAgentGoalsByUserParams {
	var archived, terminal any
	if filter.Archived {
		archived = int64(1)
	} else if filter.Terminal != nil {
		// Terminal only narrows the active set; archived rows are listed whole.
		if *filter.Terminal {
			terminal = int64(1)
		} else {
			terminal = int64(0)
		}
	}
	return sqlc.ListAgentGoalsByUserParams{
		UserID:    userID,
		Archived:  archived,
		AgentID:   nilIfEmpty(filter.AgentID),
		ProjectID: nilIfEmpty(filter.ProjectID),
		Status:    nilIfEmpty(filter.Status),
		Terminal:  terminal,
		Search:    nilIfEmpty(filter.Search),
		Limit:     limit,
		Offset:    offset,
	}
}

// ListGoals returns goals owned by the given user, newest first.
func (f *ServiceFacade) ListGoals(ctx context.Context, userID string, filter GoalFilter, limit, offset int64) ([]sqlc.AgentGoal, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentGoalsByUser(ctx, filter.params(userID, limit, offset))
}

// CountGoals returns the total goals matching the filter, ignoring pagination.
func (f *ServiceFacade) CountGoals(ctx context.Context, userID string, filter GoalFilter) (int64, error) {
	p := filter.params(userID, 0, 0)
	return f.q.CountAgentGoalsByUser(ctx, sqlc.CountAgentGoalsByUserParams{
		UserID:    p.UserID,
		Archived:  p.Archived,
		AgentID:   p.AgentID,
		ProjectID: p.ProjectID,
		Status:    p.Status,
		Terminal:  p.Terminal,
		Search:    p.Search,
	})
}

// ArchiveGoal hides a terminal/draft goal and its terminal/draft children from default lists while preserving audit data.
// All status fetches and checks run inside the tx so a concurrent transition is
// respected; re-archiving an already-archived goal is a no-op (idempotent).
func (f *ServiceFacade) ArchiveGoal(ctx context.Context, goalID string, actor Actor) error {
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		g, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if g.ArchivedAt.Valid {
			return nil
		}
		if !isArchivableGoalStatus(g.Status) {
			return ErrInvalidTransition
		}
		children, err := q.ListChildrenByGoal(ctx, nullable(goalID))
		if err != nil {
			return err
		}
		// Validate every child up front so an active child aborts the whole
		// operation before any row is archived (all-or-nothing).
		for _, child := range children {
			if !child.ArchivedAt.Valid && !isArchivableTaskStatus(child.Status) {
				return ErrInvalidTransition
			}
		}
		// Record which children THIS cascade actually archived (archiveTaskTx is a
		// no-op for already-archived ones). UnarchiveGoal restores exactly this
		// set, so children the user archived independently stay hidden.
		archivedChildIDs := make([]string, 0, len(children))
		for _, child := range children {
			archived, err := f.archiveTaskTx(ctx, q, child.ID, goalID, `{"mode":"archive","parent_goal_archived":true}`, actor)
			if err != nil {
				return err
			}
			if archived {
				archivedChildIDs = append(archivedChildIDs, child.ID)
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		n, err := q.ArchiveAgentGoal(ctx, sqlc.ArchiveAgentGoalParams{ArchivedAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: goalID})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		return f.svc.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			GoalID:     nullable(goalID),
			EventType:  "goal_archive",
			FromStatus: nullable(g.Status),
			ToStatus:   nullable(g.Status),
			ActorType:  actor.Type,
			ActorID:    nullable(actor.ID),
			Detail:     archiveGoalDetail(archivedChildIDs),
		})
	})
}

// UnarchiveGoal restores an archived goal and its archived children to default
// lists, reversing ArchiveGoal. Restoring an already-active goal is a no-op.
func (f *ServiceFacade) UnarchiveGoal(ctx context.Context, goalID string, actor Actor) error {
	return f.svc.WithTx(ctx, func(q *sqlc.Queries) error {
		g, err := getGoalForUpdate(ctx, q, goalID)
		if err != nil {
			return err
		}
		if !g.ArchivedAt.Valid {
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		children, err := q.ListChildrenByGoal(ctx, nullable(goalID))
		if err != nil {
			return err
		}
		restore, err := f.childrenArchivedByGoal(ctx, q, goalID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if !child.ArchivedAt.Valid || !restore[child.ID] {
				continue
			}
			if _, err := f.unarchiveTaskTx(ctx, q, child.ID, goalID, `{"mode":"unarchive","parent_goal_unarchived":true}`, actor); err != nil {
				return err
			}
		}
		n, err := q.UnarchiveAgentGoal(ctx, sqlc.UnarchiveAgentGoalParams{UpdatedAt: now, ID: goalID})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		return f.svc.appendEvent(ctx, q, sqlc.InsertAgentTaskEventParams{
			GoalID:     nullable(goalID),
			EventType:  "goal_unarchive",
			FromStatus: nullable(g.Status),
			ToStatus:   nullable(g.Status),
			ActorType:  actor.Type,
			ActorID:    nullable(actor.ID),
			Detail:     `{"mode":"unarchive"}`,
		})
	})
}

// goalArchiveDetail is the shape of a goal_archive event's detail JSON. The
// recorded child IDs let UnarchiveGoal reverse exactly this cascade.
type goalArchiveDetail struct {
	Mode            string   `json:"mode"`
	ArchivedTaskIDs []string `json:"archived_task_ids"`
}

func archiveGoalDetail(archivedChildIDs []string) string {
	b, err := json.Marshal(goalArchiveDetail{Mode: "archive", ArchivedTaskIDs: archivedChildIDs})
	if err != nil {
		// archivedChildIDs is plain strings; marshaling cannot fail.
		return `{"mode":"archive"}`
	}
	return string(b)
}

// childrenArchivedByGoal returns the set of child task IDs the goal's latest
// archive cascade hid. Goals archived before this detail was recorded yield an
// empty set, so their children stay archived on unarchive (safe: never restores
// a task the user did not expect).
func (f *ServiceFacade) childrenArchivedByGoal(ctx context.Context, q *sqlc.Queries, goalID string) (map[string]bool, error) {
	detail, err := q.GetLatestGoalArchiveDetail(ctx, nullable(goalID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	set := map[string]bool{}
	if detail != "" {
		var d goalArchiveDetail
		if err := json.Unmarshal([]byte(detail), &d); err != nil {
			return nil, err
		}
		for _, id := range d.ArchivedTaskIDs {
			set[id] = true
		}
	}
	return set, nil
}

func (f *ServiceFacade) CompleteGoal(ctx context.Context, goalID, output string, actor Actor) error {
	return f.svc.CompleteGoal(ctx, goalID, output, actor)
}

func isArchivableGoalStatus(status string) bool {
	switch status {
	case GoalStatusDraft, GoalStatusDone, GoalStatusFailed, GoalStatusCancelled:
		return true
	default:
		return false
	}
}

func isArchivableTaskStatus(status string) bool {
	switch status {
	case StatusDraft, StatusDone, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// ActivateGoal / CancelGoal are thin shims over TransitionService.
func (f *ServiceFacade) ActivateGoal(ctx context.Context, goalID string, actor Actor) error {
	return f.svc.ActivateGoal(ctx, goalID, actor)
}

func (f *ServiceFacade) CancelGoal(ctx context.Context, goalID, reason string, actor Actor) error {
	return f.svc.CancelGoal(ctx, goalID, reason, actor)
}

// ListGoalTasks lists child tasks of a goal.
func (f *ServiceFacade) ListGoalTasks(ctx context.Context, goalID string, limit, offset int64) ([]sqlc.AgentTask, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListChildrenByGoalPaged(ctx, sqlc.ListChildrenByGoalPagedParams{
		GoalID: nullable(goalID),
		Limit:  limit,
		Offset: offset,
	})
}

// ListGoalReviews lists reviews for a goal.
func (f *ServiceFacade) ListGoalReviews(ctx context.Context, goalID string, limit, offset int64) ([]sqlc.AgentReview, error) {
	if limit <= 0 {
		limit = 50
	}
	return f.q.ListAgentReviewsByGoal(ctx, sqlc.ListAgentReviewsByGoalParams{
		GoalID: nullable(goalID),
		Limit:  limit,
		Offset: offset,
	})
}
