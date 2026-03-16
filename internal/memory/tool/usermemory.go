package tool

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/toolspec"
)

// UserMemoryTool allows an agent to read/write per-user notes stored
// in the user_agent_memory table.
type UserMemoryTool struct {
	memStore *memory.UserMemoryStore
	userID   int64
	agentID  string
}

// NewUserMemoryTool creates a user_memory tool scoped to a specific user-agent pair.
func NewUserMemoryTool(memStore *memory.UserMemoryStore, userID int64, agentID string) *UserMemoryTool {
	return &UserMemoryTool{
		memStore: memStore,
		userID:   userID,
		agentID:  agentID,
	}
}

func (t *UserMemoryTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        "user_memory",
		Description: "Read or write persistent per-user notes. Use this to remember user preferences, context, and important details across sessions. The 'read' action returns current notes; 'write' replaces all notes with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"read", "write"},
					"description": "Action to perform: 'read' returns current memory, 'write' replaces it.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write (required for 'write' action).",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *UserMemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return "", fmt.Errorf("user_memory: action is required")
	}

	switch action {
	case "read":
		content, err := t.memStore.Get(ctx, t.userID, t.agentID)
		if err != nil {
			return "", fmt.Errorf("user_memory read: %w", err)
		}
		if content == "" {
			return "No user memory stored yet.", nil
		}
		return content, nil

	case "write":
		content, _ := args["content"].(string)
		if content == "" {
			return "", fmt.Errorf("user_memory write: content is required")
		}
		if err := t.memStore.Set(ctx, t.userID, t.agentID, content); err != nil {
			return "", fmt.Errorf("user_memory write: %w", err)
		}
		return "User memory updated successfully.", nil

	default:
		return "", fmt.Errorf("user_memory: unknown action %q, use 'read' or 'write'", action)
	}
}
