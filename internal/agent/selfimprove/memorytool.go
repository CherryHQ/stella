package selfimprove

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/pkg/tools"
)

var reviewMemoryInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["get", "update"],
      "description": "Action to perform: 'get' reads the current user memory, 'update' replaces it with new content"
    },
    "content": {
      "type": "string",
      "description": "Full updated user memory content (required for update). Always merge with existing content — do not discard what is already there."
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// ReviewMemoryTool is a restricted memory tool that only allows get/update
// of per-user persistent notes. It is used by the self-improvement reviewer.
type ReviewMemoryTool struct {
	memStore *memory.UserMemoryStore
	userID   int64
	agentID  string
}

// NewReviewMemoryTool creates a ReviewMemoryTool for the given user and agent.
func NewReviewMemoryTool(memStore *memory.UserMemoryStore, userID int64, agentID string) *ReviewMemoryTool {
	return &ReviewMemoryTool{
		memStore: memStore,
		userID:   userID,
		agentID:  agentID,
	}
}

// Definition returns the tool definition for the LLM.
func (t *ReviewMemoryTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "review_memory",
		Description: "Read and update persistent per-user memory. Use 'get' to read current content before updating, then 'update' with merged content.",
		InputSchema: reviewMemoryInputSchema,
	}
}

// Execute runs the review memory tool action.
func (t *ReviewMemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)

	switch action {
	case "get":
		return t.get(ctx)
	case "update":
		return t.update(ctx, args)
	default:
		return "", fmt.Errorf("unknown action %q, expected get/update", action)
	}
}

func (t *ReviewMemoryTool) get(ctx context.Context) (string, error) {
	content, err := t.memStore.Get(ctx, t.userID, t.agentID)
	if err != nil {
		return "", fmt.Errorf("get user memory: %w", err)
	}
	if content == "" {
		return "No existing memory.", nil
	}
	return content, nil
}

func (t *ReviewMemoryTool) update(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("content is required for update action")
	}

	if err := t.memStore.Set(ctx, t.userID, t.agentID, content); err != nil {
		return "", fmt.Errorf("update user memory: %w", err)
	}
	return "User memory updated.", nil
}
