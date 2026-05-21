package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// workerConfig holds dependencies needed to run a task worker goroutine.
type workerConfig struct {
	q             *sqlc.Queries
	mem           memory.Provider
	runnerFactory RunnerFactoryFn
}

// runWorker executes a single task session. It claims the task (pending→running),
// runs the agent loop, and ensures the task ends in a terminal state.
func runWorker(ctx context.Context, cancel context.CancelFunc, cfg workerConfig, task sqlc.AgentTask) {
	log := slog.With("component", "tasks.worker", "task_id", task.ID)

	// Atomically claim the task: pending → running.
	now := time.Now().Format(time.RFC3339)
	if err := cfg.q.UpdateAgentTaskStatusFrom(ctx, sqlc.UpdateAgentTaskStatusFromParams{
		Status:    "running",
		UpdatedAt: now,
		ID:        task.ID,
		UserID:    task.UserID,
		Status_2:  "pending",
	}); err != nil {
		log.Error("failed to claim task", "error", err)
		return
	}
	// Verify the claim succeeded (UpdateAgentTaskStatusFrom returns nil even on 0 rows).
	claimed, err := cfg.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: task.ID, UserID: task.UserID})
	if err != nil {
		log.Error("failed to verify task claim", "error", err)
		return
	}
	if claimed.Status != "running" {
		log.Info("task already claimed by another worker, skipping")
		return
	}

	// Log a "started" event.
	startedAt := time.Now().Format(time.RFC3339)
	_, _ = cfg.q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    task.ID,
		EventType: "started",
		Detail:    "{}",
		CreatedAt: startedAt,
		UpdatedAt: startedAt,
	})

	// Resolve factory for this task's agent.
	agentID := task.AgentID.String
	factory, ok := cfg.runnerFactory(agentID)
	if !ok {
		log.Error("no runner factory for agent", "agent_id", agentID)
		markFailed(ctx, cfg.q, task.ID, task.UserID, fmt.Sprintf("no runner available for agent %q", agentID))
		return
	}

	// Build the control tool with the worker's cancel func.
	controlTool := newTaskControlTool(cfg.q, task.ID, task.UserID, cancel)

	// Create a full runner (with sandbox tools) via the agent factory,
	// injecting task_control as an extra tool.
	runner, err := factory(ctx, agent.RunnerParams{
		UserID:     task.UserID,
		AgentID:    agentID,
		Memory:     cfg.mem,
		ExtraTools: []tools.Tool{controlTool},
	})
	if err != nil {
		log.Error("create runner failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, task.UserID, fmt.Sprintf("create runner: %v", err))
		return
	}
	defer func() { _ = runner.Close() }()

	// Set up memory session.
	session := taskSession(task)
	memCtx := memory.WithSessionID(ctx, session.ID)
	memCtx = memory.WithUserID(memCtx, task.UserID)
	memCtx = memory.WithAgentID(memCtx, agentID)

	if err := saveTaskSessionInfo(memCtx, cfg.mem, task, session); err != nil {
		log.Error("memory session info failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, task.UserID, fmt.Sprintf("memory session info failed: %v", err))
		return
	}
	if err := cfg.mem.Bootstrap(memCtx, session); err != nil {
		log.Error("memory bootstrap failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, task.UserID, fmt.Sprintf("memory bootstrap failed: %v", err))
		return
	}

	// Assemble prior history from memory.
	history, err := cfg.mem.Assemble(memCtx, session, 100_000, 20)
	if err != nil {
		log.Error("assemble history failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, task.UserID, fmt.Sprintf("assemble history: %v", err))
		return
	}

	// Override the runner's system prompt to include async-task instructions.
	taskCtx := agent.WithSystemOverride(memCtx, taskSystemPrompt(runner.SystemPrompt()))

	// Build the initial or resume message.
	var message string
	if len(history) == 0 {
		message = taskStartMessage(task)
	} else {
		message = taskResumeMessage(task)
	}

	log.Info("worker starting agent loop")
	eventCh := runner.Chat(taskCtx, history, message)

	// Drain events. Use a fresh context for cleanup so cancellation by
	// task_control doesn't break persistence.
	cleanupCtx := context.Background()
	cleanupCtx = memory.WithSessionID(cleanupCtx, session.ID)
	cleanupCtx = memory.WithUserID(cleanupCtx, task.UserID)
	cleanupCtx = memory.WithAgentID(cleanupCtx, agentID)

	var runErr error
	for evt := range eventCh {
		if evt.Err != nil {
			runErr = evt.Err
		}
		if evt.Store != nil {
			if appendErr := cfg.mem.Append(cleanupCtx, session, evt.Store); appendErr != nil {
				log.Warn("failed to persist message", "error", appendErr)
			}
		}
	}

	// If the task is still running (agent exited without calling task_control), finalize it.
	final, err := cfg.q.GetAgentTask(cleanupCtx, sqlc.GetAgentTaskParams{ID: task.ID, UserID: task.UserID})
	if err != nil {
		log.Error("failed to read final task status", "error", err)
		return
	}

	if final.Status == "running" {
		if runErr != nil {
			log.Error("agent loop failed", "error", runErr)
			markFailed(cleanupCtx, cfg.q, task.ID, task.UserID, runErr.Error())
		} else {
			log.Info("agent loop finished without explicit task_control done call, marking done")
			markDone(cleanupCtx, cfg.q, task.ID, task.UserID)
		}
	}
}

func taskSystemPrompt(base string) string {
	return fmt.Sprintf(`%s

# Async task mode

Use task_control for task state: progress at checkpoints, block when user input is needed, request_review before risky/user-visible changes, done when complete, and failed when you cannot continue. Keep context compact and do not notify the user directly.`, base)
}

func taskStartMessage(task sqlc.AgentTask) string {
	return fmt.Sprintf("Task ID: %s\nTask: %s\n\nDescription: %s\n\nStored context: %s", task.ID, task.Title, task.Description, task.Context)
}

func taskResumeMessage(task sqlc.AgentTask) string {
	return fmt.Sprintf("[Resume] Task ID: %s\nTask: %s\nStatus before resume: %s\nStored context: %s\n\nContinue from the persisted conversation and this task context.", task.ID, task.Title, task.Status, task.Context)
}

func markFailed(ctx context.Context, q *sqlc.Queries, taskID, userID, reason string) {
	now := time.Now().Format(time.RFC3339)
	_ = q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status:    "failed",
		UpdatedAt: now,
		ID:        taskID,
		UserID:    userID,
	})
	_, _ = q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    taskID,
		EventType: "failed",
		Detail:    detailJSON(reason),
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func markDone(ctx context.Context, q *sqlc.Queries, taskID, userID string) {
	now := time.Now().Format(time.RFC3339)
	_ = q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status:    "done",
		UpdatedAt: now,
		ID:        taskID,
		UserID:    userID,
	})
	_, _ = q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    taskID,
		EventType: "done",
		Detail:    "{}",
		CreatedAt: now,
		UpdatedAt: now,
	})
}
