package task

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/pkg/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

// TaskTool lets agents create and query async tasks.
type TaskTool struct {
	svc *tasks.Service
}

// TaskDefinition returns the task tool schema without requiring a live service.
func TaskDefinition() tools.Definition {
	return (&TaskTool{}).Definition()
}

// Definition returns the tool schema.
func (t *TaskTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "task",
		Description: "Create and query async tasks that run independently. Use for long-running work that shouldn't block the current conversation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"create", "list", "get"},
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Task title (required for create)",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Task description",
				},
				"priority": map[string]any{
					"type": "string",
					"enum": []string{"routine", "urgent"},
				},
				"agent_id": map[string]any{
					"type": "string",
				},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task ID (required for get)",
				},
				"status": map[string]any{
					"type":        "string",
					"description": "Status filter for list",
				},
				"deps": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task IDs that must be done before this task runs (optional, for create)",
				},
			},
			"required": []string{"action"},
		},
	}
}

// Execute runs the tool action.
func (t *TaskTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)

	switch action {
	case "create":
		return t.create(ctx, args)
	case "list":
		return t.list(ctx, args)
	case "get":
		return t.get(ctx, args)
	default:
		return "", fmt.Errorf("task: unknown action %q", action)
	}
}

func (t *TaskTool) create(ctx context.Context, args map[string]any) (string, error) {
	title, _ := args["title"].(string)
	if title == "" {
		return "", fmt.Errorf("task: title is required for create")
	}
	description, _ := args["description"].(string)
	priority, _ := args["priority"].(string)
	agentID, _ := args["agent_id"].(string)

	var deps []string
	if raw, ok := args["deps"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				deps = append(deps, s)
			}
		}
	}

	userID := memory.UserIDFromContext(ctx)
	if agentID == "" {
		agentID = memory.AgentIDFromContext(ctx)
	}

	task, err := t.svc.CreateTask(ctx, tasks.CreateTaskParams{
		Title:       title,
		Description: description,
		Priority:    priority,
		AgentID:     agentID,
		UserID:      userID,
		Deps:        deps,
	})
	if err != nil {
		return "", fmt.Errorf("task: create: %w", err)
	}

	result := map[string]any{
		"task": task,
		"url":  "/tasks/" + task.ID,
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("task: marshal result: %w", err)
	}
	return string(out), nil
}

func (t *TaskTool) list(ctx context.Context, args map[string]any) (string, error) {
	userID := memory.UserIDFromContext(ctx)
	status, _ := args["status"].(string)

	taskList, err := t.svc.ListTasks(ctx, userID, false, status)
	if err != nil {
		return "", fmt.Errorf("task: list: %w", err)
	}

	out, err := json.Marshal(taskList)
	if err != nil {
		return "", fmt.Errorf("task: marshal result: %w", err)
	}
	return string(out), nil
}

func (t *TaskTool) get(ctx context.Context, args map[string]any) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task: task_id is required for get")
	}

	task, err := t.svc.GetTask(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("task: get: %w", err)
	}

	out, err := json.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("task: marshal result: %w", err)
	}
	return string(out), nil
}
