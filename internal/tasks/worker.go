package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pkgagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// workerConfig holds dependencies needed to run a task worker goroutine.
type workerConfig struct {
	q             *sqlc.Queries
	mem           memory.Provider
	stream        providers.StreamFunc
	model         ai.Model
	system        string
	registry      *tools.Registry
	hooks         *hooks.HookSet
	toolLifecycle *pkgagent.ToolLifecycle
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
		Status_2:  "pending",
	}); err != nil {
		log.Error("failed to claim task", "error", err)
		return
	}
	// Verify the claim succeeded (UpdateAgentTaskStatusFrom returns nil even on 0 rows).
	claimed, err := cfg.q.GetAgentTask(ctx, task.ID)
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
		Detail:    "",
		CreatedAt: startedAt,
		UpdatedAt: startedAt,
	})

	// Build the control tool with the worker's cancel func.
	controlTool := newTaskControlTool(cfg.q, task.ID, cancel)

	// Build ToolSet from parent registry + control tool.
	toolSet := pkgagent.ToolSetFromRegistry(cfg.registry)
	toolSet[controlToolName] = pkgagent.WrapTool(controlTool)
	toolDefs := append(cfg.registry.Definitions(), controlTool.Definition())

	// Set up memory session.
	session := memory.Session{
		ID:      "task:" + task.ID,
		UserID:  task.UserID,
		AgentID: task.AgentID.String,
	}

	memCtx := memory.WithSessionID(ctx, session.ID)
	memCtx = memory.WithUserID(memCtx, task.UserID)
	memCtx = memory.WithAgentID(memCtx, task.AgentID.String)

	if err := cfg.mem.Bootstrap(memCtx, session); err != nil {
		log.Error("memory bootstrap failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, fmt.Sprintf("memory bootstrap failed: %v", err))
		return
	}

	hookMeta := hooks.HookMeta{
		SessionID: session.ID,
		UserID:    task.UserID,
		AgentID:   task.AgentID.String,
	}

	runner, err := pkgagent.NewRunner(pkgagent.RunnerConfig{
		Stream:          cfg.stream,
		Model:           cfg.model,
		Tools:           toolSet,
		ToolDefinitions: toolDefs,
	},
		pkgagent.WithSystem(cfg.system),
		pkgagent.WithHooks(cfg.hooks, hookMeta),
		pkgagent.WithToolLifecycle(cfg.toolLifecycle),
	)
	if err != nil {
		log.Error("create runner failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, fmt.Sprintf("create runner: %v", err))
		return
	}

	// Assemble prior history from memory.
	history, err := cfg.mem.Assemble(memCtx, session, 100_000, 20)
	if err != nil {
		log.Error("assemble history failed", "error", err)
		markFailed(ctx, cfg.q, task.ID, fmt.Sprintf("assemble history: %v", err))
		return
	}

	var messages []ai.Message

	if len(history) == 0 {
		// First run: system is set on runner via WithSystem; inject task description as first user message.
		messages = []ai.Message{
			ai.UserMessage{Content: fmt.Sprintf("Task: %s\n\nDescription: %s", task.Title, task.Description)},
		}
	} else {
		// Resume: include history plus a resume prompt.
		resumeMsg := ai.UserMessage{
			Content: fmt.Sprintf(
				"[Resume] Task %q (id: %s) is being resumed. Continue from where you left off.",
				task.Title, task.ID,
			),
		}
		messages = make([]ai.Message, len(history)+1)
		copy(messages, history)
		messages[len(history)] = resumeMsg
	}

	log.Info("worker starting agent loop")
	newHistory, runErr := runner.Run(memCtx, messages, nil)

	// Persist the new messages from this run.
	if len(newHistory) > len(history) {
		appended := newHistory[len(history):]
		if appendErr := cfg.mem.Append(memCtx, session, appended...); appendErr != nil {
			log.Warn("failed to persist messages", "error", appendErr)
		}
	}

	// If the task is still running (agent exited without calling task_control), finalize it.
	final, err := cfg.q.GetAgentTask(ctx, task.ID)
	if err != nil {
		log.Error("failed to read final task status", "error", err)
		return
	}

	if final.Status == "running" {
		if runErr != nil {
			log.Error("agent loop failed", "error", runErr)
			markFailed(ctx, cfg.q, task.ID, runErr.Error())
		} else {
			log.Info("agent loop finished without explicit task_control done call, marking done")
			markDone(ctx, cfg.q, task.ID)
		}
	}
}

func markFailed(ctx context.Context, q *sqlc.Queries, taskID, reason string) {
	now := time.Now().Format(time.RFC3339)
	_ = q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status:    "failed",
		UpdatedAt: now,
		ID:        taskID,
	})
	_, _ = q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    taskID,
		EventType: "failed",
		Detail:    reason,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func markDone(ctx context.Context, q *sqlc.Queries, taskID string) {
	now := time.Now().Format(time.RFC3339)
	_ = q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
		Status:    "done",
		UpdatedAt: now,
		ID:        taskID,
	})
	_, _ = q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    taskID,
		EventType: "done",
		Detail:    "",
		CreatedAt: now,
		UpdatedAt: now,
	})
}
