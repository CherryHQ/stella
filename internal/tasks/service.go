package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/notify"
	pkgagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

const defaultMaxConcurrency = 5

// Service manages task lifecycle: creation, dispatching workers, and actions.
type Service struct {
	q        *sqlc.Queries
	db       *sql.DB
	notifier notify.Notifier
	mem      memory.Provider

	stream        providers.StreamFunc
	model         ai.Model
	system        string
	registry      *tools.Registry
	hooks         *hooks.HookSet
	toolLifecycle *pkgagent.ToolLifecycle

	sem chan struct{}
	wg  sync.WaitGroup
	mu  sync.Mutex
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
	Stream         providers.StreamFunc
	Model          ai.Model
	System         string
	Registry       *tools.Registry
	Hooks          *hooks.HookSet
	ToolLifecycle  *pkgagent.ToolLifecycle
	MaxConcurrency int // 0 = default 5
}

// CreateTaskParams holds parameters for creating a new task.
type CreateTaskParams struct {
	Title       string
	Description string
	Priority    string
	AgentID     string
	UserID      int64
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
		q:             cfg.Queries,
		db:            cfg.DB,
		notifier:      cfg.Notifier,
		mem:           cfg.Memory,
		stream:        cfg.Stream,
		model:         cfg.Model,
		system:        cfg.System,
		registry:      cfg.Registry,
		hooks:         cfg.Hooks,
		toolLifecycle: cfg.ToolLifecycle,
		sem:           make(chan struct{}, concurrency),
		workers:       make(map[string]context.CancelFunc),
		log:           slog.With("component", "tasks.service"),
	}
}

// Start initialises the service lifecycle: re-dispatches stale running tasks,
// then polls for pending tasks every 30s.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Reset any tasks that were "running" when the process died, so they can retry.
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
		}); err != nil {
			s.log.Warn("failed to reset running task", "task_id", t.ID, "error", err)
		}
	}

	// Dispatch any currently pending tasks.
	pending, err := s.q.ListPendingAgentTasks(s.ctx)
	if err != nil {
		return fmt.Errorf("tasks: list pending: %w", err)
	}
	for _, t := range pending {
		s.dispatch(t)
	}

	// Poll loop.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.pollPending()
			}
		}
	}()

	return nil
}

// Stop cancels the service context and waits for all running workers to finish.
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// CreateTask inserts a new task and dispatches it immediately.
func (s *Service) CreateTask(ctx context.Context, params CreateTaskParams) (sqlc.AgentTask, error) {
	priority := params.Priority
	if priority == "" {
		priority = "normal"
	}
	id := newID()
	now := time.Now().Format(time.RFC3339)
	task, err := s.q.CreateAgentTask(ctx, sqlc.CreateAgentTaskParams{
		ID:            id,
		Title:         params.Title,
		Description:   params.Description,
		Status:        "pending",
		Priority:      priority,
		SessionID:     "task:" + id,
		Context:       "{}",
		ReviewRequest: "{}",
		AgentID:       params.AgentID,
		UserID:        params.UserID,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: create: %w", err)
	}
	s.dispatch(task)
	return task, nil
}

// GetTask returns a single task by ID.
func (s *Service) GetTask(ctx context.Context, id string) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, id)
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get %s: %w", id, err)
	}
	return task, nil
}

// ListTasks returns tasks filtered by optional userID and status.
// isAdmin = true returns all users' tasks.
func (s *Service) ListTasks(ctx context.Context, userID int64, isAdmin bool, status string) ([]sqlc.AgentTask, error) {
	if isAdmin {
		if status != "" {
			return s.q.ListAgentTasksByStatus(ctx, status)
		}
		return s.q.ListAgentTasks(ctx)
	}
	tasks, err := s.q.ListAgentTasksByUser(ctx, userID)
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
func (s *Service) UpdateTask(ctx context.Context, id string, userID int64, isAdmin bool, update UpdateTaskParams) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, id)
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get for update: %w", err)
	}
	if !isAdmin && task.UserID != userID {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: forbidden")
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
		agentID = task.AgentID
	}
	if err := s.q.UpdateAgentTask(ctx, sqlc.UpdateAgentTaskParams{
		Title:       title,
		Description: desc,
		Priority:    priority,
		AgentID:     agentID,
		UpdatedAt:   time.Now().Format(time.RFC3339),
		ID:          id,
	}); err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: update: %w", err)
	}
	return s.q.GetAgentTask(ctx, id)
}

// DeleteTask deletes a task (must be non-running or admin).
func (s *Service) DeleteTask(ctx context.Context, id string, userID int64, isAdmin bool) error {
	task, err := s.q.GetAgentTask(ctx, id)
	if err != nil {
		return fmt.Errorf("tasks: get for delete: %w", err)
	}
	if !isAdmin && task.UserID != userID {
		return fmt.Errorf("tasks: forbidden")
	}
	// Cancel a running worker if present.
	s.mu.Lock()
	cancel, running := s.workers[id]
	s.mu.Unlock()
	if running {
		cancel()
	}
	return s.q.DeleteAgentTask(ctx, id)
}

// HandleAction processes approve/reject/respond/cancel actions on a task.
func (s *Service) HandleAction(ctx context.Context, id string, userID int64, isAdmin bool, action ActionParams) (sqlc.AgentTask, error) {
	task, err := s.q.GetAgentTask(ctx, id)
	if err != nil {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: get for action: %w", err)
	}
	if !isAdmin && task.UserID != userID {
		return sqlc.AgentTask{}, fmt.Errorf("tasks: forbidden")
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
			// Worker cleanup sets status=cancelled when context is cancelled.
			if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
				Status:    "cancelled",
				UpdatedAt: now,
				ID:        id,
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
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: clear review_request: %w", err)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "pending",
			UpdatedAt: now,
			ID:        id,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: approve set pending: %w", err)
		}
		updated, err := s.q.GetAgentTask(ctx, id)
		if err != nil {
			return sqlc.AgentTask{}, err
		}
		s.dispatch(updated)

	case "reject":
		if task.Status != "review_requested" {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: reject requires review_requested status, got %q", task.Status)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "failed",
			UpdatedAt: now,
			ID:        id,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: reject: %w", err)
		}
		_, _ = s.q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
			ID:        newID(),
			TaskID:    id,
			EventType: "rejected",
			Detail:    action.Message,
			CreatedAt: now,
		})

	case "respond":
		if task.Status != "blocked" {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond requires blocked status, got %q", task.Status)
		}
		// Store the response in memory so the resumed worker sees it.
		session := memory.Session{
			ID:      "task:" + id,
			UserID:  task.UserID,
			AgentID: task.AgentID,
		}
		memCtx := memory.WithSessionID(ctx, session.ID)
		memCtx = memory.WithUserID(memCtx, task.UserID)
		memCtx = memory.WithAgentID(memCtx, task.AgentID)
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
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond clear review_request: %w", err)
		}
		if err := s.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "pending",
			UpdatedAt: now,
			ID:        id,
		}); err != nil {
			return sqlc.AgentTask{}, fmt.Errorf("tasks: respond set pending: %w", err)
		}
		updated, err := s.q.GetAgentTask(ctx, id)
		if err != nil {
			return sqlc.AgentTask{}, err
		}
		s.dispatch(updated)

	default:
		return sqlc.AgentTask{}, fmt.Errorf("tasks: unknown action %q", action.Action)
	}

	return s.q.GetAgentTask(ctx, id)
}

// dispatch acquires a semaphore slot and launches a worker goroutine for task.
// It returns immediately; the goroutine runs in the background.
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

		// Acquire semaphore.
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			return
		}
		defer func() { <-s.sem }()

		cfg := workerConfig{
			q:             s.q,
			notifier:      s.notifier,
			mem:           s.mem,
			stream:        s.stream,
			model:         s.model,
			system:        s.system,
			registry:      s.registry,
			hooks:         s.hooks,
			toolLifecycle: s.toolLifecycle,
		}
		runWorker(workerCtx, workerCancel, cfg, task)
	})
}

// pollPending dispatches any newly pending tasks that aren't already running.
func (s *Service) pollPending() {
	pending, err := s.q.ListPendingAgentTasks(s.ctx)
	if err != nil {
		s.log.Warn("poll pending failed", "error", err)
		return
	}
	s.mu.Lock()
	alreadyRunning := make(map[string]bool, len(s.workers))
	for id := range s.workers {
		alreadyRunning[id] = true
	}
	s.mu.Unlock()

	for _, t := range pending {
		if !alreadyRunning[t.ID] {
			s.dispatch(t)
		}
	}
}

// newID generates a new UUID string.
func newID() string {
	return uuid.New().String()
}
