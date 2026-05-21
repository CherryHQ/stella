package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
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
	Title          string
	Description    string
	Priority       string
	AgentID        string
	UserID         string
	Deps           []string
	SchedulerJobID string
	SchedulerRunID string
}

// UpdateTaskParams holds parameters for updating task metadata.
type UpdateTaskParams struct {
	Title       string
	Description string
	Priority    string
	AgentID     string
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
		maxConcurrency: concurrency,
		workers:        make(map[string]context.CancelFunc),
		log:            slog.With("component", "tasks.service"),
	}
}

// Start initialises the service: resets stale running tasks to pending on startup.
// The caller is responsible for scheduling Tick on a recurring interval.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Reset tasks that were "running" when the process died so they can retry.
	running, err := s.q.ListRunningAgentTasks(s.ctx)
	if err != nil {
		return fmt.Errorf("tasks: list running: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	for _, t := range running {
		if err := s.q.UpdateAgentTaskStatus(s.ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "pending",
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
	case "review_requested":
		return fmt.Sprintf("Task %q requests review: %s%s", t.Title, detail, link)
	default:
		return fmt.Sprintf("Task %q (%s): %s%s", t.Title, t.Status, detail, link)
	}
}

// sweepDepFailures transitions pending tasks to blocked when any dep has failed/cancelled.
func (s *Service) sweepDepFailures(ctx context.Context, now string) {
	pending, err := s.q.ListPendingAgentTasks(ctx)
	if err != nil {
		s.log.Warn("dep failure sweep: list failed", "error", err)
		return
	}
	for _, t := range pending {
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
				_ = s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
					Status:    "blocked",
					UpdatedAt: now,
					ID:        t.ID,
					UserID:    t.UserID,
				})
				_ = s.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
					NotifyAt:  sql.NullString{String: now, Valid: true},
					UpdatedAt: now,
					ID:        t.ID,
					UserID:    t.UserID,
				})
				_, _ = s.q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
					ID:        newID(),
					TaskID:    t.ID,
					EventType: "blocked",
					Detail:    detailJSON(fmt.Sprintf("dependency %q is %s", dep.Title, dep.Status)),
					CreatedAt: now,
					UpdatedAt: now,
				})
				break
			}
		}
	}
}

// sweepDispatch dispatches eligible pending tasks.
func (s *Service) sweepDispatch(ctx context.Context) {
	pending, err := s.q.ListPendingAgentTasks(ctx)
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

	for _, t := range pending {
		if alreadyRunning[t.ID] {
			continue
		}
		// Per-user concurrency check from DB.
		count, err := s.q.CountRunningAgentTasksByUser(ctx, t.UserID)
		if err != nil || count >= int64(s.maxConcurrency) {
			continue
		}
		// Deps must all be done.
		if !s.depsAllDone(ctx, t) {
			continue
		}
		s.dispatch(t)
		alreadyRunning[t.ID] = true
	}
}

// depsAllDone returns true if all dep tasks are in "done" status.
func (s *Service) depsAllDone(ctx context.Context, t sqlc.AgentTask) bool {
	depIDs, err := s.q.ListAgentTaskDeps(ctx, t.ID)
	if err != nil {
		return false
	}
	for _, depID := range depIDs {
		dep, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: depID, UserID: t.UserID})
		if err != nil || dep.Status != "done" {
			return false
		}
	}
	return true
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
	task, err := s.q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
		ID:             id,
		Title:          params.Title,
		Description:    params.Description,
		Status:         "pending",
		Priority:       priority,
		SessionID:      sql.NullString{String: "task:" + id, Valid: true},
		Context:        "{}",
		ReviewRequest:  "{}",
		SchedulerJobID: sql.NullString{String: params.SchedulerJobID, Valid: params.SchedulerJobID != ""},
		SchedulerRunID: sql.NullString{String: params.SchedulerRunID, Valid: params.SchedulerRunID != ""},
		AgentID:        sql.NullString{String: params.AgentID, Valid: params.AgentID != ""},
		UserID:         params.UserID,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: create: %w", err)
	}

	// Insert dependency edges.
	for _, depID := range params.Deps {
		_ = s.q.InsertAgentTaskDep(ctx, sqlc.InsertAgentTaskDepParams{
			TaskID: id,
			DepID:  depID,
		})
	}

	return task, nil
}

// validateDeps checks that all dep IDs exist and that adding them introduces no cycle.
func (s *Service) validateDeps(ctx context.Context, userID string, deps []string) error {
	for _, id := range deps {
		if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID}); err != nil {
			return fmt.Errorf("tasks: dep %q not found: %w", id, err)
		}
	}
	// DFS to detect cycles in the transitive dep graph.
	inStack := make(map[string]bool)
	var dfs func(id string) error
	dfs = func(id string) error {
		if inStack[id] {
			return fmt.Errorf("tasks: cycle detected involving task %q", id)
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

// GetTask returns a single task by ID.
func (s *Service) GetTask(ctx context.Context, id string, userID string) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, UserID: userID})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get %s: %w", id, err)
	}
	return task, nil
}

// ListTasks returns tasks filtered by userID, agentID, and status.
func (s *Service) ListTasks(ctx context.Context, userID, agentID, status string) ([]sqlc.AgentTask, error) {
	var tasks []sqlc.AgentTask
	var err error

	if agentID != "" {
		tasks, err = s.q.ListAgentTasksByUserAndAgent(ctx, sqlc.ListAgentTasksByUserAndAgentParams{
			UserID:  userID,
			AgentID: sql.NullString{String: agentID, Valid: true},
		})
	} else {
		tasks, err = s.q.ListAgentTasksByUser(ctx, userID)
	}
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
	agentID := update.AgentID
	if agentID == "" {
		agentID = task.AgentID.String
	}
	if err := s.q.UpdateAgentTask(ctx, sqlc.UpdateAgentTaskParams{
		Title:       title,
		Description: desc,
		Priority:    priority,
		AgentID:     sql.NullString{String: agentID, Valid: agentID != ""},
		UpdatedAt:   time.Now().Format(time.RFC3339),
		ID:          id,
		UserID:      userID,
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
		switch task.Status {
		case "pending", "blocked", "review_requested":
			if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
				Status:    "cancelled",
				UpdatedAt: now,
				ID:        id,
				UserID:    userID,
			}); err != nil {
				return sqlc.AgentTask{}, fmt.Errorf("tasks: cancel: %w", err)
			}
		case "running":
			s.mu.Lock()
			cancel, ok := s.workers[id]
			s.mu.Unlock()
			if ok {
				cancel()
			}
			if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
				Status:    "cancelled",
				UpdatedAt: now,
				ID:        id,
				UserID:    userID,
			}); err != nil {
				return sqlc.AgentTask{}, fmt.Errorf("tasks: cancel running: %w", err)
			}
		default:
			return sqlc.AgentTask{}, fmt.Errorf("tasks: cannot cancel task in status %q", task.Status)
		}

	case "approve":
		if task.Status != "review_requested" {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: approve requires review_requested status, got %q", task.Status)
		}
		if err := s.q.UpdateAgentTaskReviewRequest(ctx, sqlc.UpdateAgentTaskReviewRequestParams{
			ReviewRequest: "{}",
			UpdatedAt:     now,
			ID:            id,
			UserID:        userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: clear review_request: %w", err)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "pending",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: approve set pending: %w", err)
		}

	case "reject":
		if task.Status != "review_requested" {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: reject requires review_requested status, got %q", task.Status)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "failed",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: reject: %w", err)
		}
		_, _ = s.q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
			ID:        newID(),
			TaskID:    id,
			EventType: "failed",
			Detail:    detailJSON(action.Message),
			CreatedAt: now,
			UpdatedAt: now,
		})

	case "respond":
		if task.Status != "blocked" {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond requires blocked status, got %q", task.Status)
		}
		session := taskSession(task)
		memCtx := memory.WithSessionID(ctx, session.ID)
		memCtx = memory.WithUserID(memCtx, task.UserID)
		memCtx = memory.WithAgentID(memCtx, task.AgentID.String)
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
			Status:    "pending",
			UpdatedAt: now,
			ID:        id,
			UserID:    userID,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond set pending: %w", err)
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
		return fmt.Errorf("tasks: task %q not found: %w", taskID, err)
	}
	if task.Status != "pending" && task.Status != "blocked" {
		return fmt.Errorf("tasks: cannot add dep to task in status %q", task.Status)
	}
	if _, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: depID, UserID: userID}); err != nil {
		return fmt.Errorf("tasks: dep %q not found: %w", depID, err)
	}
	if taskID == depID {
		return fmt.Errorf("tasks: cannot depend on self")
	}
	// Cycle detection: DFS from depID — if we reach taskID, reject.
	visited := make(map[string]bool)
	var dfs func(id string) error
	dfs = func(id string) error {
		if id == taskID {
			return fmt.Errorf("tasks: cycle detected — adding this dependency would create a loop")
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
		return fmt.Errorf("tasks: task %q not found: %w", taskID, err)
	}
	if err := s.q.DeleteAgentTaskDep(ctx, sqlc.DeleteAgentTaskDepParams{TaskID: taskID, DepID: depID}); err != nil {
		return fmt.Errorf("tasks: remove dep: %w", err)
	}
	if task.Status == "blocked" && s.depsAllDone(ctx, task) {
		now := time.Now().Format(time.RFC3339)
		_ = s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status: "pending", UpdatedAt: now, ID: taskID, UserID: userID,
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
		return TaskDeps{}, fmt.Errorf("tasks: task %q not found: %w", taskID, err)
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
		if t.AgentID.Valid && t.AgentID.String == agentID {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

// BatchTaskItem holds parameters for one task in a batch create.
type BatchTaskItem struct {
	Title       string
	Description string
	Priority    string
	AgentID     string
	DraftID     string
	Deps        []string
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
					return nil, fmt.Errorf("tasks: dep %q not found", dep)
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
			return fmt.Errorf("tasks: cycle detected in batch")
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
			ID:            realIDs[i],
			Title:         t.Title,
			Description:   t.Description,
			Status:        "pending",
			Priority:      priority,
			SessionID:     sql.NullString{String: "task:" + realIDs[i], Valid: true},
			Context:       "{}",
			ReviewRequest: "{}",
			AgentID:       sql.NullString{String: t.AgentID, Valid: t.AgentID != ""},
			UserID:        params.UserID,
			CreatedAt:     now,
			UpdatedAt:     now,
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

// ListTaskDeps returns the dep IDs for a task (used by API layer to populate deps field).
func (s *Service) ListTaskDeps(ctx context.Context, taskID string) ([]string, error) {
	return s.q.ListAgentTaskDeps(ctx, taskID)
}

// dispatch launches a worker goroutine for task. It returns immediately.
func (s *Service) dispatch(task sqlc.AgentTask) {
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
			q:             s.q,
			mem:           s.mem,
			runnerFactory: s.runnerFactory,
		}
		runWorker(workerCtx, workerCancel, cfg, task)
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

// newID generates a new UUID string.
func newID() string {
	return uuid.New().String()
}
