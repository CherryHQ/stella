package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrCycle         = errors.New("cycle detected")
	ErrInvalidStatus = errors.New("invalid status")
)

const defaultMaxConcurrency = 5

// RunnerFactoryFn resolves a runner factory for a given agent ID.
// Returns false if the agent has no pool (task will be skipped).
type RunnerFactoryFn func(agentID string) (agent.NewRunnerFunc, bool)

// Service manages task lifecycle: creation, dispatching workers, and actions.
type Service struct {
	db            *sql.DB
	q             *sqlc.Queries
	notifier      notify.Notifier
	mem           memory.Provider
	runnerFactory RunnerFactoryFn
	scheduler     *Scheduler

	maxConcurrency int
	wg             sync.WaitGroup
	mu             sync.Mutex
	// workers maps taskID → cancel func for running workers.
	workers map[string]context.CancelFunc

	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
}

// Config holds construction parameters for Service.
type Config struct {
	DB             *sql.DB
	Queries        *sqlc.Queries
	Notifier       notify.Notifier
	Memory         memory.Provider
	RunnerFactory  RunnerFactoryFn
	MaxConcurrency int // 0 = default 5
}

// CreateTaskParams holds parameters for creating a new task.
type CreateTaskParams struct {
	Title           string
	Description     string
	Priority        string
	AssigneeAgentID string
	UserID          string
	Deps            []string
	SchedulerJobID  string
	SchedulerRunID  string
	ReviewPolicy    string // auto, agent, human; empty defaults to auto
}

// CreateGoalParams holds parameters for creating a goal (parent task).
type CreateGoalParams struct {
	Title           string
	Description     string
	Priority        string
	AssigneeAgentID string
	UserID          string
}

// ChildTaskInput describes one child task in a SplitTask call.
type ChildTaskInput struct {
	Title           string
	Description     string
	Priority        string
	AssigneeAgentID string
	Required        bool
	ReviewPolicy    string
	Deps            []string // draft IDs of other children in the same batch
	Criteria        []string // acceptance criterion descriptions
}

// ReviewDecision holds parameters for HandleReviewDecision.
type ReviewDecision struct {
	Status   string // approved, changes_requested, rejected
	Summary  string
	Feedback string
	Items    []ReviewItemInput
}

// ReviewItemInput holds per-criterion evidence in a review decision.
type ReviewItemInput struct {
	CriterionID string
	Passed      bool
	Evidence    string
}

// UpdateTaskParams holds parameters for updating task metadata.
type UpdateTaskParams struct {
	Title           string
	Description     string
	Priority        string
	AssigneeAgentID string
}

// ActionParams holds parameters for task action handling.
type ActionParams struct {
	Action  string // cancel, approve, reject, respond
	Message string // reason or response message
}

// New creates a Service from cfg.
func New(cfg Config) *Service {
	concurrency := cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = defaultMaxConcurrency
	}
	return &Service{
		db:             cfg.DB,
		q:              cfg.Queries,
		notifier:       cfg.Notifier,
		mem:            cfg.Memory,
		runnerFactory:  cfg.RunnerFactory,
		scheduler:      NewScheduler(cfg.Queries, concurrency),
		maxConcurrency: concurrency,
		workers:        make(map[string]context.CancelFunc),
		log:            slog.With("component", "tasks.service"),
	}
}

// Start initialises the service: interrupts stale runs and resets orphaned tasks on startup.
// The caller is responsible for scheduling Tick on a recurring interval.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	now := time.Now().Format(time.RFC3339)

	// Interrupt any runs that were in-flight when the process died.
	if err := s.q.InterruptStaleRuns(s.ctx, sqlc.InterruptStaleRunsParams{
		UpdatedAt: now,
		StartedAt: sql.NullString{String: now, Valid: true},
	}); err != nil {
		s.log.Warn("failed to interrupt stale runs", "error", err)
	}

	// Reset tasks that were "running" when the process died so they can retry.
	running, err := s.q.ListRunningAgentTasks(s.ctx)
	if err != nil {
		return fmt.Errorf("tasks: list running: %w", err)
	}
	for _, t := range running {
		if err := s.q.UpdateAgentTaskStatus(s.ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "ready",
			UpdatedAt: now,
			ID:        t.ID,
			UserID:    t.UserID,
		}); err != nil {
			s.log.Warn("failed to reset running task", "task_id", t.ID, "error", err)
		}
	}

	return nil
}

// Stop cancels the service context and waits for all running workers to finish.
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// Tick is called by the external scheduler on a recurring interval.
// It performs three sweeps in order: notify, dep-failure check, dispatch.
func (s *Service) Tick() {
	ctx := s.ctx
	if ctx == nil {
		return
	}
	nowStr := time.Now().Format(time.RFC3339)

	s.sweepNotifications(ctx, nowStr)
	s.sweepDepFailures(ctx, nowStr)
	s.sweepDispatch(ctx)
}

// sweepNotifications sends pending notifications and clears notify_at.
func (s *Service) sweepNotifications(ctx context.Context, now string) {
	tasks, err := s.q.ListPendingNotifyTasks(ctx, sql.NullString{String: now, Valid: true})
	if err != nil {
		s.log.Warn("notify sweep: list failed", "error", err)
		return
	}
	for _, t := range tasks {
		msg := s.notifyMessage(ctx, t)
		if s.notifier != nil {
			_ = s.notifier.Notify(ctx, pkgchannel.Notification{Text: msg})
		}
		_ = s.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
			NotifyAt:  sql.NullString{Valid: false},
			UpdatedAt: now,
			ID:        t.ID,
			UserID:    t.UserID,
		})
	}
}

// notifyMessage builds a notification string from task status and latest event.
func (s *Service) notifyMessage(ctx context.Context, t sqlc.AgentTask) string {
	events, err := s.q.ListAgentTaskEvents(ctx, t.ID)
	detail := ""
	if err == nil && len(events) > 0 {
		detail = events[len(events)-1].Detail
	}
	link := ""
	if base := strings.TrimRight(os.Getenv("STELLA_PUBLIC_URL"), "/"); base != "" {
		link = " " + base + "/tasks/" + t.ID
	}
	switch t.Status {
	case "blocked":
		return fmt.Sprintf("Task %q is blocked: %s%s", t.Title, detail, link)
	case "reviewing":
		return fmt.Sprintf("Task %q requests review: %s%s", t.Title, detail, link)
	default:
		return fmt.Sprintf("Task %q (%s): %s%s", t.Title, t.Status, detail, link)
	}
}

// sweepDepFailures transitions ready and draft tasks to blocked/failed when any dep has failed/cancelled.
func (s *Service) sweepDepFailures(ctx context.Context, now string) {
	ready, err := s.q.ListReadyAgentTasks(ctx)
	if err != nil {
		s.log.Warn("dep failure sweep: list ready failed", "error", err)
	}
	drafts, err := s.q.ListDraftAgentTasksWithDeps(ctx)
	if err != nil {
		s.log.Warn("dep failure sweep: list drafts failed", "error", err)
	}

	candidates := make([]sqlc.AgentTask, 0, len(ready)+len(drafts))
	candidates = append(candidates, ready...)
	candidates = append(candidates, drafts...)
	for _, t := range candidates {
		depRows, err := s.q.ListAgentTaskDeps(ctx, t.ID)
		if err != nil || len(depRows) == 0 {
			continue
		}
		for _, depID := range depRows {
			dep, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: depID, UserID: t.UserID})
			if err != nil {
				continue
			}
			if dep.Status == "failed" || dep.Status == "cancelled" {
				targetStatus := "blocked"
				if t.Status == "draft" {
					targetStatus = "cancelled"
				}
				_ = s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
					Status:    targetStatus,
					UpdatedAt: now,
					ID:        t.ID,
					UserID:    t.UserID,
				})
				if targetStatus == "blocked" {
					_ = s.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
						NotifyAt:  sql.NullString{String: now, Valid: true},
						UpdatedAt: now,
						ID:        t.ID,
						UserID:    t.UserID,
					})
				}
				insertEvent(ctx, s.q, t.ID, targetStatus, detailJSON(fmt.Sprintf("dependency %q is %s", dep.Title, dep.Status)))
				break
			}
		}
	}
}

// sweepDispatch dispatches eligible ready tasks via the scheduler.
func (s *Service) sweepDispatch(ctx context.Context) {
	ready, err := s.q.ListReadyAgentTasks(ctx)
	if err != nil {
		s.log.Warn("dispatch sweep: list failed", "error", err)
		return
	}

	s.mu.Lock()
	alreadyRunning := make(map[string]bool, len(s.workers))
	for id := range s.workers {
		alreadyRunning[id] = true
	}
	s.mu.Unlock()

	for _, t := range ready {
		if alreadyRunning[t.ID] {
			continue
		}
		if !s.scheduler.EligibleForWorker(ctx, t) {
			continue
		}
		run, err := s.createRun(ctx, t, "worker_run", "execution")
		if err != nil {
			s.log.Warn("dispatch: create run failed", "task_id", t.ID, "error", err)
			continue
		}
		s.dispatch(t, run)
		alreadyRunning[t.ID] = true
	}
}

// CreateTask inserts a new task. The scheduler Tick dispatches it.
func (s *Service) CreateTask(ctx context.Context, params CreateTaskParams) (sqlc.AgentTask, error) {
	priority := params.Priority
	if priority == "" {
		priority = "routine"
	}

	// Validate deps exist and no cycles.
	if err := s.validateDeps(ctx, params.UserID, params.Deps); err != nil {
		return sqlc.AgentTask{}, err
	}

	id := newID()
	now := time.Now().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	task, err := qtx.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
		ID:              id,
		RootID:          id,
		TaskType:        "task",
		Title:           params.Title,
		Description:     params.Description,
		Status:          "ready",
		Priority:        priority,
		SessionID:       sql.NullString{String: "task:" + id, Valid: true},
		Context:         "{}",
		ReviewRequest:   "{}",
		SchedulerJobID:  sql.NullString{String: params.SchedulerJobID, Valid: params.SchedulerJobID != ""},
		SchedulerRunID:  sql.NullString{String: params.SchedulerRunID, Valid: params.SchedulerRunID != ""},
		AssigneeAgentID: sql.NullString{String: params.AssigneeAgentID, Valid: params.AssigneeAgentID != ""},
		UserID:          params.UserID,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: create: %w", err)
	}

	for _, depID := range params.Deps {
		if err := qtx.InsertAgentTaskDep(ctx, sqlc.InsertAgentTaskDepParams{
			TaskID: id,
			DepID:  depID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: insert dep: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: commit: %w", err)
	}
	return task, nil
}

// validateDeps checks that all dep IDs exist and that adding them introduces no cycle.
func (s *Service) validateDeps(ctx context.Context, userID string, deps []string) error {
	for _, id := range deps {
		if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID}); err != nil {
			return fmt.Errorf("tasks: dep %q: %w", id, ErrNotFound)
		}
	}
	// DFS to detect cycles in the transitive dep graph.
	inStack := make(map[string]bool)
	var dfs func(id string) error
	dfs = func(id string) error {
		if inStack[id] {
			return fmt.Errorf("tasks: %w involving task %q", ErrCycle, id)
		}
		inStack[id] = true
		defer func() { inStack[id] = false }()
		depIDs, err := s.q.ListAgentTaskDeps(ctx, id)
		if err != nil {
			return nil
		}
		for _, depID := range depIDs {
			if err := dfs(depID); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range deps {
		if err := dfs(id); err != nil {
			return err
		}
	}
	return nil
}

// detectIntraBatchCycle checks for cycles in an intra-batch dependency graph using DFS.
func detectIntraBatchCycle(n int, edges map[int][]int) error {
	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully explored
	)
	color := make([]int, n)
	var dfs func(i int) error
	dfs = func(i int) error {
		color[i] = gray
		for _, j := range edges[i] {
			if color[j] == gray {
				return fmt.Errorf("tasks: %w in split children (index %d → %d)", ErrCycle, i, j)
			}
			if color[j] == white {
				if err := dfs(j); err != nil {
					return err
				}
			}
		}
		color[i] = black
		return nil
	}
	for i := range n {
		if color[i] == white {
			if err := dfs(i); err != nil {
				return err
			}
		}
	}
	return nil
}

// GetTask returns a single task by ID.
func (s *Service) GetTask(ctx context.Context, id string, userID string) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get %s: %w", id, err)
	}
	return task, nil
}

// ListTasks returns tasks for a specific agent, filtered by status.
func (s *Service) ListTasks(ctx context.Context, userID, agentID, status string) ([]sqlc.AgentTask, error) {
	tasks, err := s.q.ListAgentTasksByUserAndAgent(ctx, sqlc.ListAgentTasksByUserAndAgentParams{
		UserID:          userID,
		AssigneeAgentID: sql.NullString{String: agentID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}

	if status == "" {
		return tasks, nil
	}
	filtered := tasks[:0]
	for _, t := range tasks {
		if t.Status == status {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

// UpdateTask updates mutable task fields (title, description, priority, agent_id).
func (s *Service) UpdateTask(ctx context.Context, id string, userID string, update UpdateTaskParams) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get for update: %w", err)
	}
	title := update.Title
	if title == "" {
		title = task.Title
	}
	desc := update.Description
	if desc == "" {
		desc = task.Description
	}
	priority := update.Priority
	if priority == "" {
		priority = task.Priority
	}
	agentID := update.AssigneeAgentID
	if agentID == "" {
		agentID = task.AssigneeAgentID.String
	}
	if err := s.q.UpdateAgentTask(ctx, sqlc.UpdateAgentTaskParams{
		Title:           title,
		Description:     desc,
		Priority:        priority,
		AssigneeAgentID: sql.NullString{String: agentID, Valid: agentID != ""},
		UpdatedAt:       time.Now().Format(time.RFC3339),
		ID:              id,
		UserID:          userID,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: update: %w", err)
	}
	return s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
}

// DeleteTask deletes a task (cancels running worker if present).
func (s *Service) DeleteTask(ctx context.Context, id string, userID string) error {
	_, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("tasks: get for delete: %w", err)
	}
	s.mu.Lock()
	cancel, running := s.workers[id]
	s.mu.Unlock()
	if running {
		cancel()
	}
	return s.q.DeleteAgentTask(ctx, sqlc.DeleteAgentTaskParams{ID: id, UserID: userID})
}

// HandleAction processes approve/reject/respond/cancel actions on a task.
func (s *Service) HandleAction(ctx context.Context, id string, userID string, action ActionParams) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get for action: %w", err)
	}

	now := time.Now().Format(time.RFC3339)

	switch action.Action {
	case "cancel":
		if err := ValidateTaskTransition(task.TaskType, task.Status, "cancelled", RoleUser); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: %w", err)
		}
		if task.Status == "running" {
			s.mu.Lock()
			cancel, ok := s.workers[id]
			s.mu.Unlock()
			if ok {
				cancel()
			}
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "cancelled",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: cancel: %w", err)
		}
		// Cancel pending reviews.
		_ = s.q.CancelReviewsByTask(ctx, sqlc.CancelReviewsByTaskParams{
			TaskID:     id,
			UserID:     userID,
			ResolvedAt: sql.NullString{String: now, Valid: true},
		})
		insertEvent(ctx, s.q, id, "cancelled", detailJSON(action.Message))

	case "approve":
		if err := ValidateTaskTransition(task.TaskType, task.Status, "done", RoleUser); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: %w", err)
		}
		if err := s.q.UpdateAgentTaskReviewRequest(ctx, sqlc.UpdateAgentTaskReviewRequestParams{
			ReviewRequest: "{}",
			UpdatedAt:     now,
			ID:            id,
			UserID:        userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: clear review_request: %w", err)
		}
		// Cancel any pending reviews for this task.
		if err := s.q.CancelReviewsByTask(ctx, sqlc.CancelReviewsByTaskParams{
			TaskID:     id,
			UserID:     userID,
			ResolvedAt: sql.NullString{String: now, Valid: true},
		}); err != nil {
			s.log.Warn("failed to cancel pending reviews on approve", "task_id", id, "error", err)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "done",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: approve set done: %w", err)
		}
		insertEvent(ctx, s.q, id, "done", detailJSON("approved by user"))

	case "reject":
		if err := ValidateTaskTransition(task.TaskType, task.Status, "failed", RoleUser); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: %w", err)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "failed",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: reject: %w", err)
		}
		// Cancel pending reviews.
		_ = s.q.CancelReviewsByTask(ctx, sqlc.CancelReviewsByTaskParams{
			TaskID:     id,
			UserID:     userID,
			ResolvedAt: sql.NullString{String: now, Valid: true},
		})
		insertEvent(ctx, s.q, id, "failed", detailJSON(action.Message))

	case "respond":
		if err := ValidateTaskTransition(task.TaskType, task.Status, "ready", RoleUser); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: %w", err)
		}
		session := taskSession(task)
		memCtx := memory.WithSessionID(ctx, session.ID)
		memCtx = memory.WithUserID(memCtx, task.UserID)
		memCtx = memory.WithAgentID(memCtx, task.AssigneeAgentID.String)
		if err := saveTaskSessionInfo(memCtx, s.mem, task, session); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond session info: %w", err)
		}
		if err := s.mem.Bootstrap(memCtx, session); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond bootstrap: %w", err)
		}
		if err := s.mem.Append(memCtx, session, ai.UserMessage{Content: action.Message}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond append: %w", err)
		}
		if err := s.q.UpdateAgentTaskReviewRequest(ctx, sqlc.UpdateAgentTaskReviewRequestParams{
			ReviewRequest: "{}",
			UpdatedAt:     now,
			ID:            id,
			UserID:        userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond clear review_request: %w", err)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "ready",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond set ready: %w", err)
		}

	default:
		return sqlc.AgentTask{}, fmt.Errorf("tasks: unknown action %q", action.Action)
	}

	return s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
}

// ListTaskEvents returns all events for a task ordered by creation time.
func (s *Service) ListTaskEvents(ctx context.Context, taskID string) ([]sqlc.AgentTaskEvent, error) {
	return s.q.ListAgentTaskEvents(ctx, taskID)
}

// AddDep adds a dependency edge (taskID depends on depID) with cycle detection.
func (s *Service) AddDep(ctx context.Context, taskID, depID, userID string) error {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: taskID, UserID: userID})
	if err != nil {
		return fmt.Errorf("tasks: task %q: %w", taskID, ErrNotFound)
	}
	if task.Status != "ready" && task.Status != "blocked" {
		return fmt.Errorf("tasks: cannot add dep to task in status %q: %w", task.Status, ErrInvalidStatus)
	}
	if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: depID, UserID: userID}); err != nil {
		return fmt.Errorf("tasks: dep %q: %w", depID, ErrNotFound)
	}
	if taskID == depID {
		return fmt.Errorf("tasks: cannot depend on self")
	}
	// Cycle detection: DFS from depID — if we reach taskID, reject.
	visited := make(map[string]bool)
	var dfs func(id string) error
	dfs = func(id string) error {
		if id == taskID {
			return fmt.Errorf("tasks: %w — adding this dependency would create a loop", ErrCycle)
		}
		if visited[id] {
			return nil
		}
		visited[id] = true
		deps, err := s.q.ListAgentTaskDeps(ctx, id)
		if err != nil {
			return err
		}
		for _, d := range deps {
			if err := dfs(d); err != nil {
				return err
			}
		}
		return nil
	}
	if err := dfs(depID); err != nil {
		return err
	}
	return s.q.InsertAgentTaskDep(ctx, sqlc.InsertAgentTaskDepParams{TaskID: taskID, DepID: depID})
}

// RemoveDep removes a dependency edge. If the task was blocked and all remaining deps are done, transitions to pending.
func (s *Service) RemoveDep(ctx context.Context, taskID, depID, userID string) error {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: taskID, UserID: userID})
	if err != nil {
		return fmt.Errorf("tasks: task %q: %w", taskID, ErrNotFound)
	}
	if err := s.q.DeleteAgentTaskDep(ctx, sqlc.DeleteAgentTaskDepParams{TaskID: taskID, DepID: depID}); err != nil {
		return fmt.Errorf("tasks: remove dep: %w", err)
	}
	if task.Status == "blocked" && s.scheduler.DepsAllDone(ctx, task) {
		now := time.Now().Format(time.RFC3339)
		_ = s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status: "ready", UpdatedAt: now, ID: taskID, UserID: userID,
		})
	}
	return nil
}

// TaskDeps holds upstream and downstream dependency info.
type TaskDeps struct {
	Upstream   []sqlc.AgentTask
	Downstream []sqlc.AgentTask
}

// GetTaskDeps returns upstream dependencies and downstream dependents.
func (s *Service) GetTaskDeps(ctx context.Context, taskID, userID string) (TaskDeps, error) {
	if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: taskID, UserID: userID}); err != nil {
		return TaskDeps{}, fmt.Errorf("tasks: task %q: %w", taskID, ErrNotFound)
	}
	depIDs, err := s.q.ListAgentTaskDeps(ctx, taskID)
	if err != nil {
		return TaskDeps{}, fmt.Errorf("tasks: list deps: %w", err)
	}
	upstream := make([]sqlc.AgentTask, 0, len(depIDs))
	for _, id := range depIDs {
		t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
		if err == nil {
			upstream = append(upstream, t)
		}
	}
	dependentIDs, err := s.q.ListAgentTaskDependents(ctx, taskID)
	if err != nil {
		return TaskDeps{}, fmt.Errorf("tasks: list dependents: %w", err)
	}
	downstream := make([]sqlc.AgentTask, 0, len(dependentIDs))
	for _, id := range dependentIDs {
		t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
		if err == nil {
			downstream = append(downstream, t)
		}
	}
	return TaskDeps{Upstream: upstream, Downstream: downstream}, nil
}

// ListUnblockedTasks returns pending tasks whose dependencies are all done.
func (s *Service) ListUnblockedTasks(ctx context.Context, userID, agentID string) ([]sqlc.AgentTask, error) {
	tasks, err := s.q.ListUnblockedAgentTasks(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("tasks: list unblocked: %w", err)
	}
	if agentID == "" {
		return tasks, nil
	}
	filtered := tasks[:0]
	for _, t := range tasks {
		if t.AssigneeAgentID.Valid && t.AssigneeAgentID.String == agentID {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

// BatchTaskItem holds parameters for one task in a batch create.
type BatchTaskItem struct {
	Title           string
	Description     string
	Priority        string
	AssigneeAgentID string
	DraftID         string
	Deps            []string
}

// BatchCreateParams holds parameters for batch task creation.
type BatchCreateParams struct {
	UserID string
	Tasks  []BatchTaskItem
}

// BatchCreateTasks creates multiple tasks atomically with intra-batch dependency resolution.
func (s *Service) BatchCreateTasks(ctx context.Context, params BatchCreateParams) ([]sqlc.AgentTask, error) {
	if s.db == nil {
		return nil, fmt.Errorf("tasks: batch create requires db connection")
	}
	if len(params.Tasks) == 0 {
		return nil, fmt.Errorf("tasks: batch create requires at least one task")
	}

	// Assign real IDs and build draftID→realID map.
	realIDs := make([]string, len(params.Tasks))
	draftMap := make(map[string]string)
	for i, t := range params.Tasks {
		realIDs[i] = newID()
		if t.DraftID != "" {
			if _, exists := draftMap[t.DraftID]; exists {
				return nil, fmt.Errorf("tasks: duplicate draft_id %q", t.DraftID)
			}
			draftMap[t.DraftID] = realIDs[i]
		}
	}

	// Resolve deps: draft IDs to real IDs, validate external deps.
	resolvedDeps := make([][]string, len(params.Tasks))
	for i, t := range params.Tasks {
		for _, dep := range t.Deps {
			if realID, ok := draftMap[dep]; ok {
				resolvedDeps[i] = append(resolvedDeps[i], realID)
			} else {
				if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: dep, UserID: params.UserID}); err != nil {
					return nil, fmt.Errorf("tasks: dep %q: %w", dep, ErrNotFound)
				}
				resolvedDeps[i] = append(resolvedDeps[i], dep)
			}
		}
	}

	// Cycle detection on the batch graph.
	idToIdx := make(map[string]int)
	for i, id := range realIDs {
		idToIdx[id] = i
	}
	visited := make(map[string]int) // 0=unvisited, 1=in-stack, 2=done
	var dfs func(id string) error
	dfs = func(id string) error {
		if visited[id] == 1 {
			return fmt.Errorf("tasks: %w in batch", ErrCycle)
		}
		if visited[id] == 2 {
			return nil
		}
		visited[id] = 1
		if idx, ok := idToIdx[id]; ok {
			for _, dep := range resolvedDeps[idx] {
				if err := dfs(dep); err != nil {
					return err
				}
			}
		}
		visited[id] = 2
		return nil
	}
	for _, id := range realIDs {
		if err := dfs(id); err != nil {
			return nil, err
		}
	}

	// Create all tasks in a transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tasks: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	now := time.Now().Format(time.RFC3339)
	results := make([]sqlc.AgentTask, 0, len(params.Tasks))
	for i, t := range params.Tasks {
		priority := t.Priority
		if priority == "" {
			priority = "routine"
		}
		task, err := qtx.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
			ID:              realIDs[i],
			RootID:          realIDs[i],
			TaskType:        "task",
			Title:           t.Title,
			Description:     t.Description,
			Status:          "ready",
			Priority:        priority,
			SessionID:       sql.NullString{String: "task:" + realIDs[i], Valid: true},
			Context:         "{}",
			ReviewRequest:   "{}",
			AssigneeAgentID: sql.NullString{String: t.AssigneeAgentID, Valid: t.AssigneeAgentID != ""},
			UserID:          params.UserID,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if err != nil {
			return nil, fmt.Errorf("tasks: batch create task %q: %w", t.Title, err)
		}
		for _, depID := range resolvedDeps[i] {
			if err := qtx.InsertAgentTaskDep(ctx, sqlc.InsertAgentTaskDepParams{TaskID: realIDs[i], DepID: depID}); err != nil {
				return nil, fmt.Errorf("tasks: batch insert dep: %w", err)
			}
		}
		results = append(results, task)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("tasks: commit batch: %w", err)
	}
	return results, nil
}

// CreateGoal creates a goal (parent container for child tasks).
func (s *Service) CreateGoal(ctx context.Context, params CreateGoalParams) (sqlc.AgentTask, error) {
	id := newID()
	now := time.Now().Format(time.RFC3339)
	goal, err := s.q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
		ID:              id,
		RootID:          id,
		TaskType:        "goal",
		Title:           params.Title,
		Description:     params.Description,
		Status:          "draft",
		Priority:        orDefault(params.Priority, "routine"),
		SessionID:       sql.NullString{String: "goal:" + id, Valid: true},
		Context:         "{}",
		ReviewRequest:   "{}",
		AssigneeAgentID: sql.NullString{String: params.AssigneeAgentID, Valid: params.AssigneeAgentID != ""},
		UserID:          params.UserID,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: create goal: %w", err)
	}
	return goal, nil
}

// SplitTask creates draft children under a goal with deps and acceptance criteria.
func (s *Service) SplitTask(ctx context.Context, goalID, userID string, children []ChildTaskInput) ([]sqlc.AgentTask, error) {
	goal, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: goalID, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("tasks: goal %q: %w", goalID, ErrNotFound)
	}
	if goal.TaskType != "goal" {
		return nil, fmt.Errorf("tasks: %q is not a goal: %w", goalID, ErrInvalidStatus)
	}
	if goal.Status != "draft" && goal.Status != "running" {
		return nil, fmt.Errorf("tasks: cannot split goal in status %q: %w", goal.Status, ErrInvalidStatus)
	}
	if len(children) == 0 {
		return nil, fmt.Errorf("tasks: at least one child required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tasks: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	// Assign IDs and build index→realID map for intra-batch deps.
	realIDs := make([]string, len(children))
	for i := range children {
		realIDs[i] = newID()
	}

	// Resolve intra-batch deps (by index string) and validate.
	resolvedDeps := make([][]string, len(children))
	intraBatchEdges := make(map[int][]int) // adjacency list for cycle detection
	for i, c := range children {
		for _, dep := range c.Deps {
			found := false
			for j, rid := range realIDs {
				if dep == fmt.Sprintf("%d", j) || dep == rid {
					resolvedDeps[i] = append(resolvedDeps[i], rid)
					intraBatchEdges[i] = append(intraBatchEdges[i], j)
					found = true
					break
				}
			}
			if !found {
				if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: dep, UserID: userID}); err != nil {
					return nil, fmt.Errorf("tasks: dep %q: %w", dep, ErrNotFound)
				}
				resolvedDeps[i] = append(resolvedDeps[i], dep)
			}
		}
	}
	// Detect cycles in intra-batch dependency graph.
	if err := detectIntraBatchCycle(len(children), intraBatchEdges); err != nil {
		return nil, err
	}

	now := time.Now().Format(time.RFC3339)
	results := make([]sqlc.AgentTask, 0, len(children))
	for i, c := range children {
		task, err := qtx.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
			ID:              realIDs[i],
			ParentID:        sql.NullString{String: goalID, Valid: true},
			RootID:          goal.RootID,
			TaskType:        "task",
			Title:           c.Title,
			Description:     c.Description,
			Status:          "draft",
			Priority:        orDefault(c.Priority, goal.Priority),
			Required:        c.Required,
			ReviewPolicy:    sql.NullString{String: c.ReviewPolicy, Valid: c.ReviewPolicy != ""},
			SessionID:       sql.NullString{String: "task:" + realIDs[i], Valid: true},
			Context:         "{}",
			ReviewRequest:   "{}",
			AssigneeAgentID: sql.NullString{String: c.AssigneeAgentID, Valid: c.AssigneeAgentID != ""},
			CreatedByAgentID: sql.NullString{
				String: goal.AssigneeAgentID.String,
				Valid:  goal.AssigneeAgentID.Valid,
			},
			UserID:    userID,
			CreatedAt: now,
			UpdatedAt: now,
		})
		if err != nil {
			return nil, fmt.Errorf("tasks: create child %q: %w", c.Title, err)
		}
		for _, depID := range resolvedDeps[i] {
			if err := qtx.InsertAgentTaskDep(ctx, sqlc.InsertAgentTaskDepParams{TaskID: realIDs[i], DepID: depID}); err != nil {
				return nil, fmt.Errorf("tasks: insert dep: %w", err)
			}
		}
		// Create acceptance criteria.
		for pos, desc := range c.Criteria {
			_, err := qtx.CreateAcceptanceCriterion(ctx, sqlc.CreateAcceptanceCriterionParams{
				ID:          newID(),
				UserID:      userID,
				TaskID:      realIDs[i],
				Description: desc,
				Required:    true,
				Position:    int64(pos),
				CreatedAt:   now,
			})
			if err != nil {
				return nil, fmt.Errorf("tasks: create criterion: %w", err)
			}
		}
		results = append(results, task)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("tasks: commit split: %w", err)
	}
	return results, nil
}

// PlanReady atomically activates a goal and its unblocked draft children.
func (s *Service) PlanReady(ctx context.Context, goalID, userID string) (sqlc.AgentTask, error) {
	goal, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: goalID, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: goal %q: %w", goalID, ErrNotFound)
	}
	if goal.TaskType != "goal" {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: %q is not a goal: %w", goalID, ErrInvalidStatus)
	}
	if goal.Status != "draft" {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: goal must be in draft status, got %q: %w", goal.Status, ErrInvalidStatus)
	}

	now := time.Now().Format(time.RFC3339)

	// Activate goal: draft → ready.
	if err := ValidateTaskTransition("goal", goal.Status, "ready", RoleManager); err != nil {
		return sqlc.AgentTask{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status: "ready", UpdatedAt: now, ID: goalID, UserID: userID,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: activate goal: %w", err)
	}

	if err := qtx.ActivateDraftChildren(ctx, sqlc.ActivateDraftChildrenParams{
		UpdatedAt: now,
		ParentID:  sql.NullString{String: goalID, Valid: true},
		UserID:    userID,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: activate children: %w", err)
	}

	insertEvent(ctx, qtx, goalID, "plan_ready", "{}")

	if err := tx.Commit(); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: commit: %w", err)
	}
	return s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: goalID, UserID: userID})
}

// SubmitForReview transitions a task to reviewing and creates a review record.
func (s *Service) SubmitForReview(ctx context.Context, taskID, userID, runID, summary string) (sqlc.AgentTaskReview, error) {
	if runID == "" {
		return sqlc.AgentTaskReview{}, fmt.Errorf("tasks: runID is required for review submission")
	}
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: taskID, UserID: userID})
	if err != nil {
		return sqlc.AgentTaskReview{}, fmt.Errorf("tasks: task %q: %w", taskID, ErrNotFound)
	}
	if err := ValidateTaskTransition(task.TaskType, task.Status, "reviewing", RoleWorker); err != nil {
		return sqlc.AgentTaskReview{}, err
	}

	now := time.Now().Format(time.RFC3339)

	// Complete the worker run.
	if runID != "" {
		_ = s.q.CompleteRun(ctx, sqlc.CompleteRunParams{
			ResultJson: detailJSON(summary),
			FinishedAt: sql.NullString{String: now, Valid: true},
			UpdatedAt:  now,
			ID:         runID,
			UserID:     userID,
		})
	}

	// Transition task to reviewing.
	if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status: "reviewing", UpdatedAt: now, ID: taskID, UserID: userID,
	}); err != nil {
		return sqlc.AgentTaskReview{}, fmt.Errorf("tasks: set reviewing: %w", err)
	}

	// Auto-approval path.
	if task.ReviewPolicy.String == "auto" || !task.ReviewPolicy.Valid {
		review, err := s.q.CreateAgentTaskReview(ctx, sqlc.CreateAgentTaskReviewParams{
			ID:             newID(),
			UserID:         userID,
			TaskID:         taskID,
			ReviewerType:   "system",
			ReviewerID:     "auto",
			SubmittedRunID: runID,
			Status:         "approved",
			Summary:        summary,
			CreatedAt:      now,
			ResolvedAt:     sql.NullString{String: now, Valid: true},
		})
		if err != nil {
			return sqlc.AgentTaskReview{}, fmt.Errorf("tasks: create auto-review: %w", err)
		}
		// Auto-approve: reviewing → done.
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status: "done", UpdatedAt: now, ID: taskID, UserID: userID,
		}); err != nil {
			return sqlc.AgentTaskReview{}, fmt.Errorf("tasks: auto-approve done: %w", err)
		}
		insertEvent(ctx, s.q, taskID, "done", detailJSON("auto-approved"))
		return review, nil
	}

	// Create a pending review record.
	review, err := s.q.CreateAgentTaskReview(ctx, sqlc.CreateAgentTaskReviewParams{
		ID:             newID(),
		UserID:         userID,
		TaskID:         taskID,
		ReviewerType:   task.ReviewPolicy.String,
		SubmittedRunID: runID,
		Status:         "requested",
		Summary:        summary,
		CreatedAt:      now,
	})
	if err != nil {
		return sqlc.AgentTaskReview{}, fmt.Errorf("tasks: create review: %w", err)
	}
	insertEvent(ctx, s.q, taskID, "reviewing", detailJSON(summary))
	return review, nil
}

// HandleReviewDecision resolves a review and transitions the task accordingly.
func (s *Service) HandleReviewDecision(ctx context.Context, reviewID, userID string, decision ReviewDecision) (sqlc.AgentTask, error) {
	review, err := s.q.GetAgentTaskReview(ctx, sqlc.GetAgentTaskReviewParams{ID: reviewID, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: review %q: %w", reviewID, ErrNotFound)
	}
	if review.Status != "requested" {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: review already resolved (%s): %w", review.Status, ErrInvalidStatus)
	}

	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: review.TaskID, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: task: %w", ErrNotFound)
	}

	now := time.Now().Format(time.RFC3339)

	// Resolve the review record.
	if err := s.q.ResolveReview(ctx, sqlc.ResolveReviewParams{
		Status:        decision.Status,
		Summary:       decision.Summary,
		Feedback:      decision.Feedback,
		ReviewerRunID: sql.NullString{},
		ResolvedAt:    sql.NullString{String: now, Valid: true},
		ID:            reviewID,
		UserID:        userID,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: resolve review: %w", err)
	}

	// Create review items.
	for _, item := range decision.Items {
		_, err := s.q.UpsertReviewItem(ctx, sqlc.UpsertReviewItemParams{
			ID:          newID(),
			UserID:      userID,
			ReviewID:    reviewID,
			CriterionID: item.CriterionID,
			Passed:      sql.NullBool{Bool: item.Passed, Valid: true},
			Evidence:    item.Evidence,
			CreatedAt:   now,
		})
		if err != nil {
			s.log.Warn("failed to upsert review item", "criterion_id", item.CriterionID, "error", err)
		}
	}

	// Transition task based on decision.
	var newStatus string
	switch decision.Status {
	case "approved":
		newStatus = "done"
	case "changes_requested":
		newStatus = "changes_requested"
	case "rejected":
		newStatus = "failed"
	default:
		return sqlc.AgentTask{}, fmt.Errorf("tasks: unknown review decision %q", decision.Status)
	}

	if err := ValidateTaskTransition(task.TaskType, task.Status, newStatus, RoleReviewer); err != nil {
		return sqlc.AgentTask{}, err
	}
	if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status: newStatus, UpdatedAt: now, ID: task.ID, UserID: userID,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: update task status: %w", err)
	}

	// If changes_requested: auto-retry if budget remains, otherwise fail.
	finalStatus := newStatus
	if newStatus == "changes_requested" {
		if task.RetryCount < task.MaxRetries {
			if err := ValidateTaskTransition(task.TaskType, "changes_requested", "ready", RoleSystem); err != nil {
				return sqlc.AgentTask{}, err
			}
			if err := s.q.UpdateAgentTaskRetryCount(ctx, sqlc.UpdateAgentTaskRetryCountParams{
				RetryCount: task.RetryCount + 1,
				UpdatedAt:  now,
				ID:         task.ID,
				UserID:     userID,
			}); err != nil {
				s.log.Warn("failed to increment retry count", "task_id", task.ID, "error", err)
			}
			if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
				Status: "ready", UpdatedAt: now, ID: task.ID, UserID: userID,
			}); err != nil {
				s.log.Warn("failed to auto-transition to ready", "task_id", task.ID, "error", err)
			}
			finalStatus = "ready"
		} else {
			if err := ValidateTaskTransition(task.TaskType, "changes_requested", "failed", RoleSystem); err != nil {
				return sqlc.AgentTask{}, err
			}
			if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
				Status: "failed", UpdatedAt: now, ID: task.ID, UserID: userID,
			}); err != nil {
				s.log.Warn("failed to transition exhausted retries to failed", "task_id", task.ID, "error", err)
			}
			finalStatus = "failed"
			insertEvent(ctx, s.q, task.ID, "failed", detailJSON("retry budget exhausted"))
		}
	}

	insertEvent(ctx, s.q, task.ID, finalStatus, detailJSON(decision.Feedback))
	return s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: task.ID, UserID: userID})
}

// ReopenTask transitions a terminal task back to ready for another attempt.
func (s *Service) ReopenTask(ctx context.Context, taskID, userID string) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: taskID, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: task %q: %w", taskID, ErrNotFound)
	}
	if err := ValidateTaskTransition(task.TaskType, task.Status, "ready", RoleManager); err != nil {
		return sqlc.AgentTask{}, err
	}

	now := time.Now().Format(time.RFC3339)
	if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status: "ready", UpdatedAt: now, ID: taskID, UserID: userID,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: reopen: %w", err)
	}
	insertEvent(ctx, s.q, taskID, "reopened", "{}")
	return s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: taskID, UserID: userID})
}

// ListTaskDeps returns the dep IDs for a task (used by API layer to populate deps field).
func (s *Service) ListTaskDeps(ctx context.Context, taskID string) ([]string, error) {
	return s.q.ListAgentTaskDeps(ctx, taskID)
}

// ListRuns returns all runs for a task.
func (s *Service) ListRuns(ctx context.Context, taskID, userID string) ([]sqlc.AgentTaskRun, error) {
	return s.q.ListRunsByTask(ctx, sqlc.ListRunsByTaskParams{TaskID: taskID, UserID: userID})
}

// ListReviews returns all reviews for a task.
func (s *Service) ListReviews(ctx context.Context, taskID, userID string) ([]sqlc.AgentTaskReview, error) {
	return s.q.ListReviewsByTask(ctx, sqlc.ListReviewsByTaskParams{TaskID: taskID, UserID: userID})
}

// ListCriteria returns acceptance criteria for a task.
func (s *Service) ListCriteria(ctx context.Context, taskID, userID string) ([]sqlc.AgentTaskAcceptanceCriterion, error) {
	return s.q.ListCriteriaByTask(ctx, sqlc.ListCriteriaByTaskParams{TaskID: taskID, UserID: userID})
}

// CreateCriterion adds an acceptance criterion to a task.
func (s *Service) CreateCriterion(ctx context.Context, taskID, userID, description string, required bool, position int64) (sqlc.AgentTaskAcceptanceCriterion, error) {
	return s.q.CreateAcceptanceCriterion(ctx, sqlc.CreateAcceptanceCriterionParams{
		ID:          newID(),
		UserID:      userID,
		TaskID:      taskID,
		Description: description,
		Required:    required,
		Position:    position,
		CreatedAt:   time.Now().Format(time.RFC3339),
	})
}

// createRun inserts a new run record for a task.
func (s *Service) createRun(ctx context.Context, task sqlc.AgentTask, kind, purpose string) (sqlc.AgentTaskRun, error) {
	now := time.Now().Format(time.RFC3339)
	return s.q.CreateAgentTaskRun(ctx, sqlc.CreateAgentTaskRunParams{
		ID:         newID(),
		UserID:     task.UserID,
		TaskID:     task.ID,
		AgentID:    task.AssigneeAgentID,
		Kind:       kind,
		Purpose:    purpose,
		Status:     "queued",
		SessionID:  sql.NullString{String: "run:" + task.ID, Valid: true},
		ResultJson: "{}",
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// dispatch launches a worker goroutine for task with an associated run. It returns immediately.
func (s *Service) dispatch(task sqlc.AgentTask, run sqlc.AgentTaskRun) {
	workerCtx, workerCancel := context.WithCancel(s.ctx)

	s.mu.Lock()
	s.workers[task.ID] = workerCancel
	s.mu.Unlock()

	s.wg.Go(func() {
		defer func() {
			s.mu.Lock()
			delete(s.workers, task.ID)
			s.mu.Unlock()
			workerCancel()
		}()

		cfg := workerConfig{
			svc:           s,
			q:             s.q,
			mem:           s.mem,
			runnerFactory: s.runnerFactory,
		}
		runWorker(workerCtx, workerCancel, cfg, task, run)
	})
}

// detailJSON wraps a plain message string as a JSON object {"message":"..."}.
// An empty message produces "{}".
func detailJSON(msg string) string {
	if msg == "" {
		return "{}"
	}
	b, _ := json.Marshal(map[string]any{"message": msg})
	return string(b)
}

// insertEvent is a convenience wrapper around InsertAgentTaskEvent with nil run/review IDs.
func insertEvent(ctx context.Context, q *sqlc.Queries, taskID, eventType, detail string) {
	now := time.Now().Format(time.RFC3339)
	_, _ = q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    taskID,
		EventType: eventType,
		Detail:    detail,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// newID generates a new UUID string.
func newID() string {
	return uuid.New().String()
}

func orDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}
