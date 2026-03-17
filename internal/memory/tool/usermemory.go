package tool

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/toolspec"
)

// UserMemoryTool allows an agent to write per-user notes stored
// in the user_agent_memory table. Notes are automatically injected
// into the system prompt at session start, so no read action is needed.
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
		Name: "user_memory",
		Description: `Update persistent per-user notes. These notes are always present in your system prompt (in the "User Memory" section), so you already have the current content — no need to read first.

Use this to remember user preferences, high-level impressions, and important context across sessions. Keep notes concise and high-level — like how a person remembers someone they know, not every detail.

Recommended structure:
## User Preferences
How the user wants you to behave (tone, style, language, topics to focus on or avoid).

## About the User
High-level understanding: who they are, what matters to them, key context.

## Notes
Recurring topics, quirks, or anything worth remembering.

Always include the full updated content, not just a diff. The content replaces the entire user memory.`,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "Full updated user memory content. Replaces the entire existing memory.",
				},
			},
			"required": []string{"content"},
		},
	}
}

func (t *UserMemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("user_memory: content is required")
	}
	if err := t.memStore.Set(ctx, t.userID, t.agentID, content); err != nil {
		return "", fmt.Errorf("user_memory write: %w", err)
	}
	return "User memory updated. Changes will appear in your system prompt at the next session start.", nil
}
