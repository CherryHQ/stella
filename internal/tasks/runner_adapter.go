package tasks

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

// PoolLookup resolves an agent ID to a Runner factory. Returned (nil, false)
// means no pool is available for that agent; the adapter will surface this
// as a non-retryable Fail.
type PoolLookup func(agentID string) (agent.NewRunnerFunc, bool)

// PoolAdapter wires the dispatcher's RunnerFunc to the real agent.Pool. It:
//
//  1. Looks up a Runner factory for the run's executor agent.
//  2. Builds a `task_control` Tool that delegates to TaskControlTool.
//  3. Loads the task description as the user message.
//  4. Pumps Runner.Chat events until the runner finishes OR the tool fires a
//     terminal action (submit/block/fail).
//
// Session bootstrap: the dispatcher's SessionMinter handed us run.session_id.
// The Runner is given that session_id directly; memory.Provider Bootstrap is
// the runner's own responsibility when it starts (it's idempotent).
//
// The control_tool's Finished flag is the primary success signal. If the
// agent loop exits without calling the tool, the worker's protocol fallback
// (Phase 3) records a `protocol_error` event and Fails the run.
type PoolAdapter struct {
	pools PoolLookup
	mem   memory.Provider
	log   *slog.Logger
}

// NewPoolAdapter builds an adapter usable as a RunnerFunc.
func NewPoolAdapter(pools PoolLookup, mem memory.Provider, log *slog.Logger) *PoolAdapter {
	if log == nil {
		log = slog.Default().With("component", "tasks/pool-adapter")
	}
	return &PoolAdapter{pools: pools, mem: mem, log: log}
}

// AsRunnerFunc returns the dispatch hook. Wire this into BootConfig.Runner.
func (a *PoolAdapter) AsRunnerFunc(q *sqlc.Queries) RunnerFunc {
	return func(ctx context.Context, run sqlc.AgentTaskRun, tool *TaskControlTool) error {
		agentID := ""
		if run.ExecutorAgentID.Valid {
			agentID = run.ExecutorAgentID.String
		}
		if agentID == "" {
			return tool.Fail(ctx, "no executor agent on run", false)
		}
		factory, ok := a.pools(agentID)
		if !ok || factory == nil {
			return tool.Fail(ctx, fmt.Sprintf("no agent pool for %s", agentID), false)
		}
		// Load the task to build the prompt and pass context to the agent.
		taskID := ""
		if run.TaskID.Valid {
			taskID = run.TaskID.String
		}
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil {
			return tool.Fail(ctx, fmt.Sprintf("load task: %v", err), true)
		}

		ctTool := newTaskControlExternalTool(tool, a.log)
		runner, err := factory(ctx, agent.RunnerParams{
			AgentID:    agentID,
			UserID:     run.UserID,
			SessionID:  run.SessionID,
			Memory:     a.mem,
			ExtraTools: []tools.Tool{ctTool},
		})
		if err != nil {
			return tool.Fail(ctx, fmt.Sprintf("create runner: %v", err), true)
		}
		defer func() { _ = runner.Close() }()

		prompt := buildTaskPrompt(task)
		events := runner.Chat(ctx, nil, prompt)
		for ev := range events {
			if ev.Err != nil {
				a.log.Warn("task runner event error",
					"task_id", taskID, "run_id", run.ID, "err", ev.Err)
				if !tool.Finished() {
					return tool.Fail(ctx, fmt.Sprintf("runner error: %v", ev.Err), true)
				}
				return ev.Err
			}
			if tool.Finished() {
				// Drain remaining events so the runner can close cleanly,
				// but don't act on them.
				go drainEvents(events)
				return nil
			}
		}
		// Channel closed without a terminal tool call. Phase 3's worker will
		// apply the protocol-error fallback.
		return nil
	}
}

func drainEvents(ch <-chan agent.Event) {
	for range ch {
	}
}

// buildTaskPrompt assembles the user-message string for the runner. It's
// kept terse and predictable so the agent always sees the same shape.
func buildTaskPrompt(task sqlc.AgentTask) string {
	if task.Description == "" {
		return task.Title
	}
	return task.Title + "\n\n" + task.Description
}

// taskControlExternalTool exposes the TaskControlTool to the agent's tool
// registry under the name `task_control`. The agent calls it with an
// `action` argument and the relevant payload.
type taskControlExternalTool struct {
	tool *TaskControlTool
	log  *slog.Logger
}

func newTaskControlExternalTool(t *TaskControlTool, log *slog.Logger) *taskControlExternalTool {
	return &taskControlExternalTool{tool: t, log: log}
}

func (t *taskControlExternalTool) Definition() tools.Definition {
	return ai.ToolDefinition{
		Name: "task_control",
		Description: "Report task lifecycle. Call exactly one of submit/block/fail before exiting. " +
			"Use progress freely while working.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"progress", "submit", "block", "fail"},
					"description": "Which lifecycle action to take.",
				},
				"patch": map[string]any{
					"type":        "object",
					"description": "progress: shallow-merge JSON into task.context.",
				},
				"output": map[string]any{
					"type":        "object",
					"description": "submit: final output written to task.output.",
				},
				"kind": map[string]any{
					"type":        "string",
					"description": "block: blocker kind (user_input|external_dependency|tool_error|policy_hold).",
				},
				"question": map[string]any{
					"type":        "string",
					"description": "block: human-readable explanation of what's needed.",
				},
				"detail": map[string]any{
					"type":        "object",
					"description": "block/fail: structured detail.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "fail: error message.",
				},
				"retryable": map[string]any{
					"type":        "boolean",
					"description": "fail: true if dispatcher should retry within budget.",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *taskControlExternalTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "progress":
		patch := args["patch"]
		if patch == nil {
			patch = map[string]any{}
		}
		if err := t.tool.Progress(ctx, patch); err != nil {
			return "", err
		}
		return `{"ok":true}`, nil
	case "submit":
		output := args["output"]
		if output == nil {
			output = map[string]any{}
		}
		if err := t.tool.Submit(ctx, output); err != nil {
			return "", err
		}
		return `{"ok":true,"status":"done"}`, nil
	case "block":
		kind, _ := args["kind"].(string)
		question, _ := args["question"].(string)
		if err := t.tool.Block(ctx, kind, question, args["detail"]); err != nil {
			return "", err
		}
		return `{"ok":true,"status":"blocked"}`, nil
	case "fail":
		reason, _ := args["reason"].(string)
		retryable, _ := args["retryable"].(bool)
		if err := t.tool.Fail(ctx, reason, retryable); err != nil {
			return "", err
		}
		return `{"ok":true,"status":"failed"}`, nil
	default:
		return "", fmt.Errorf("task_control: unknown action %q", action)
	}
}
