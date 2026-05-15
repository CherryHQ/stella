package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const controlToolName = "task_control"

// TaskControlTool is injected into task sessions.
// Its Execute method performs DB transitions directly.
type TaskControlTool struct {
	q      *sqlc.Queries
	taskID string
	cancel context.CancelFunc
}

func newTaskControlTool(q *sqlc.Queries, taskID string, cancel context.CancelFunc) *TaskControlTool {
	return &TaskControlTool{
		q:      q,
		taskID: taskID,
		cancel: cancel,
	}
}

func (t *TaskControlTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        controlToolName,
		Description: "Signal task state transitions. Use to report progress, request a human review, block awaiting input, or mark the task done or failed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"progress", "block", "request_review", "done", "failed"},
					"description": "The state transition to perform.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Required for block, request_review, done, and failed. A human-readable description of the state.",
				},
				"context": map[string]any{
					"type":        "object",
					"description": "Optional. For progress: updates context metadata stored with the task.",
				},
				"review_request": map[string]any{
					"type":        "object",
					"description": "Optional. For request_review: structured review request payload.",
				},
				"notify_after": map[string]any{
					"type":        "string",
					"description": "Optional. For progress: a Go duration (e.g. '2h') after which the user is notified.",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *TaskControlTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	message, _ := args["message"].(string)
	now := time.Now().Format(time.RFC3339)

	switch action {
	case "progress":
		contextObj, _ := args["context"].(map[string]any)
		contextJSON := "{}"
		if contextObj != nil {
			b, err := json.Marshal(contextObj)
			if err != nil {
				return "", fmt.Errorf("task_control: marshal context: %w", err)
			}
			contextJSON = string(b)
		}
		if err := t.q.UpdateAgentTaskContext(ctx, sqlc.UpdateAgentTaskContextParams{
			Context:   contextJSON,
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: update context: %w", err)
		}
		// Optional deferred notification.
		if notifyAfter, _ := args["notify_after"].(string); notifyAfter != "" {
			d, err := time.ParseDuration(notifyAfter)
			if err == nil {
				notifyAt := time.Now().Add(d).Format(time.RFC3339)
				_ = t.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
					NotifyAt:  sql.NullString{String: notifyAt, Valid: true},
					UpdatedAt: now,
					ID:        t.taskID,
				})
			}
		}
		if err := t.logEvent(ctx, "progress", message); err != nil {
			return "", err
		}
		return "progress recorded", nil

	case "block":
		if err := t.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "blocked",
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set blocked: %w", err)
		}
		if err := t.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
			NotifyAt:  sql.NullString{String: now, Valid: true},
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set notify_at: %w", err)
		}
		if err := t.logEvent(ctx, "blocked", message); err != nil {
			return "", err
		}
		t.cancel()
		return "task blocked", nil

	case "request_review":
		reviewObj, _ := args["review_request"].(map[string]any)
		reviewJSON := "{}"
		if reviewObj != nil {
			b, err := json.Marshal(reviewObj)
			if err != nil {
				return "", fmt.Errorf("task_control: marshal review_request: %w", err)
			}
			reviewJSON = string(b)
		}
		if err := t.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "review_requested",
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set review_requested: %w", err)
		}
		if err := t.q.UpdateAgentTaskReviewRequest(ctx, sqlc.UpdateAgentTaskReviewRequestParams{
			ReviewRequest: reviewJSON,
			UpdatedAt:     now,
			ID:            t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: update review_request: %w", err)
		}
		if err := t.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
			NotifyAt:  sql.NullString{String: now, Valid: true},
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set notify_at: %w", err)
		}
		if err := t.logEvent(ctx, "review_requested", message); err != nil {
			return "", err
		}
		t.cancel()
		return "review requested", nil

	case "done":
		if message != "" {
			outputJSON, err := json.Marshal(map[string]any{"output": message})
			if err != nil {
				return "", fmt.Errorf("task_control: marshal output: %w", err)
			}
			if err := t.q.UpdateAgentTaskContext(ctx, sqlc.UpdateAgentTaskContextParams{
				Context:   string(outputJSON),
				UpdatedAt: now,
				ID:        t.taskID,
			}); err != nil {
				return "", fmt.Errorf("task_control: store output: %w", err)
			}
		}
		if err := t.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "done",
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set done: %w", err)
		}
		if err := t.logEvent(ctx, "done", message); err != nil {
			return "", err
		}
		t.cancel()
		return "task marked done", nil

	case "failed":
		if err := t.q.UpdateAgentTaskStatus(ctx, sqlc.UpdateAgentTaskStatusParams{
			Status:    "failed",
			UpdatedAt: now,
			ID:        t.taskID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set failed: %w", err)
		}
		if err := t.logEvent(ctx, "failed", message); err != nil {
			return "", err
		}
		t.cancel()
		return "task marked failed", nil

	default:
		return "", fmt.Errorf("task_control: unknown action %q", action)
	}
}

func (t *TaskControlTool) logEvent(ctx context.Context, eventType, detail string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := t.q.InsertAgentTaskEvent(ctx, sqlc.InsertAgentTaskEventParams{
		ID:        newID(),
		TaskID:    t.taskID,
		EventType: eventType,
		Detail:    detailJSON(detail),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("task_control: log event: %w", err)
	}
	return nil
}
