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

// TaskControlTool is injected into worker task sessions.
// It knows the current run ID and routes terminal actions through the service.
type TaskControlTool struct {
	svc    *Service
	q      *sqlc.Queries
	taskID string
	runID  string
	userID string
	cancel context.CancelFunc
}

func newTaskControlTool(svc *Service, q *sqlc.Queries, taskID, runID, userID string, cancel context.CancelFunc) *TaskControlTool {
	return &TaskControlTool{
		svc:    svc,
		q:      q,
		taskID: taskID,
		runID:  runID,
		userID: userID,
		cancel: cancel,
	}
}

func (t *TaskControlTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        controlToolName,
		Description: "Signal task state transitions. Use progress for checkpoints, block when user input is needed, submit_for_review when work is complete, or failed when you cannot continue.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"progress", "block", "submit_for_review", "failed"},
					"description": "The state transition to perform.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Required for block, submit_for_review, and failed. A human-readable summary.",
				},
				"context": map[string]any{
					"type":        "object",
					"description": "Optional. For progress: updates context metadata stored with the task.",
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
			UserID:    t.userID,
		}); err != nil {
			return "", fmt.Errorf("task_control: update context: %w", err)
		}
		if notifyAfter, _ := args["notify_after"].(string); notifyAfter != "" {
			d, err := time.ParseDuration(notifyAfter)
			if err == nil {
				notifyAt := time.Now().Add(d).Format(time.RFC3339)
				_ = t.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
					NotifyAt:  sql.NullString{String: notifyAt, Valid: true},
					UpdatedAt: now,
					ID:        t.taskID,
					UserID:    t.userID,
				})
			}
		}
		if err := t.logEvent(ctx, "progress", message); err != nil {
			return "", err
		}
		return "progress recorded", nil

	case "block":
		if err := ValidateTaskTransition("task", "running", "blocked", RoleWorker); err != nil {
			return "", fmt.Errorf("task_control: %w", err)
		}
		if err := t.q.UpdateAgentTaskStatusFrom(ctx, sqlc.UpdateAgentTaskStatusFromParams{
			Status:    "blocked",
			UpdatedAt: now,
			ID:        t.taskID,
			UserID:    t.userID,
			Status_2:  "running",
		}); err != nil {
			return "", fmt.Errorf("task_control: set blocked: %w", err)
		}
		if t.runID != "" {
			_ = t.q.FailRun(ctx, sqlc.FailRunParams{
				Error:      "blocked: " + message,
				FinishedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:  now,
				ID:         t.runID,
				UserID:     t.userID,
			})
		}
		if err := t.q.UpdateAgentTaskNotifyAt(ctx, sqlc.UpdateAgentTaskNotifyAtParams{
			NotifyAt:  sql.NullString{String: now, Valid: true},
			UpdatedAt: now,
			ID:        t.taskID,
			UserID:    t.userID,
		}); err != nil {
			return "", fmt.Errorf("task_control: set notify_at: %w", err)
		}
		if err := t.logEvent(ctx, "blocked", message); err != nil {
			return "", err
		}
		t.cancel()
		return "task blocked", nil

	case "submit_for_review":
		if _, err := t.svc.SubmitForReview(ctx, t.taskID, t.userID, t.runID, message); err != nil {
			return "", fmt.Errorf("task_control: submit for review: %w", err)
		}
		t.cancel()
		return "submitted for review", nil

	case "failed":
		if err := ValidateTaskTransition("task", "running", "failed", RoleWorker); err != nil {
			return "", fmt.Errorf("task_control: %w", err)
		}
		if err := t.q.UpdateAgentTaskStatusFrom(ctx, sqlc.UpdateAgentTaskStatusFromParams{
			Status:    "failed",
			UpdatedAt: now,
			ID:        t.taskID,
			UserID:    t.userID,
			Status_2:  "running",
		}); err != nil {
			return "", fmt.Errorf("task_control: set failed: %w", err)
		}
		if t.runID != "" {
			_ = t.q.FailRun(ctx, sqlc.FailRunParams{
				Error:      message,
				FinishedAt: sql.NullString{String: now, Valid: true},
				UpdatedAt:  now,
				ID:         t.runID,
				UserID:     t.userID,
			})
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
