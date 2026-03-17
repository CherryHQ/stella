package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/toolspec"
)

var memoryInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["grep", "describe", "expand", "user_memory_update"],
      "description": "The action to perform"
    },
    "pattern": {
      "type": "string",
      "description": "Text pattern to search for (required for grep, case-insensitive substring match)"
    },
    "scope": {
      "type": "string",
      "enum": ["messages", "summaries", "both"],
      "description": "Where to search: 'messages', 'summaries', or 'both' (default). Only for grep"
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of results to return (default 20). Only for grep"
    },
    "summary_id": {
      "type": "string",
      "description": "The summary ID to inspect or expand (required for describe and expand)"
    },
    "token_cap": {
      "type": "integer",
      "description": "Maximum tokens of content to return (default 4000). Only for expand"
    },
    "content": {
      "type": "string",
      "description": "Full updated user memory content. Replaces the entire existing memory (required for user_memory_update)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// MemoryTool provides a unified interface for all memory operations.
// Actions: grep, describe, expand, user_memory_update.
type MemoryTool struct {
	engine   memory.Engine
	memStore *memory.UserMemoryStore
}

// NewMemoryTool creates a unified memory tool.
func NewMemoryTool(engine memory.Engine, memStore *memory.UserMemoryStore) *MemoryTool {
	return &MemoryTool{engine: engine, memStore: memStore}
}

// MemoryDefinition returns the tool definition without requiring a live engine.
func MemoryDefinition() toolspec.Definition {
	return toolspec.Definition{
		Name: "memory",
		Description: `Manage conversation history and per-user persistent notes.

Actions:
- grep: Search conversation history for messages and summaries matching a pattern. Use to recall earlier discussions, decisions, or code changes.
- describe: Inspect a summary's content, metadata, and lineage (parents/children). Use after grep returns summary results to understand context.
- expand: Drill into a summary to retrieve original messages (leaf) or child summaries (condensed). Use to recover details lost during compaction.
- user_memory_update: Update persistent per-user notes. These notes are always present in the system prompt (in the "User Memory" section). Use to remember user preferences, impressions, and important context across sessions. Always include the full updated content — it replaces the entire user memory.`,
		InputSchema: memoryInputSchema,
	}
}

func (t *MemoryTool) Definition() toolspec.Definition {
	return MemoryDefinition()
}

func (t *MemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "grep":
		return t.grep(ctx, args)
	case "describe":
		return t.describe(ctx, args)
	case "expand":
		return t.expand(ctx, args)
	case "user_memory_update":
		return t.userMemoryUpdate(ctx, args)
	default:
		return "", fmt.Errorf("unknown action %q, expected grep/describe/expand/user_memory_update", action)
	}
}

func (t *MemoryTool) grep(ctx context.Context, args map[string]any) (string, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("memory grep: pattern is required")
	}

	scope, _ := args["scope"].(string)
	limit := intArg(args, "limit", 0)

	sessionID := memory.SessionIDFromContext(ctx)
	if sessionID == "" {
		return "", fmt.Errorf("memory grep: no session context")
	}

	results, err := t.engine.Retrieval().GrepBySession(ctx, sessionID, pattern, scope, limit)
	if err != nil {
		return "", fmt.Errorf("memory grep: %w", err)
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory grep: marshal: %w", err)
	}
	return string(out), nil
}

func (t *MemoryTool) describe(ctx context.Context, args map[string]any) (string, error) {
	summaryID, ok := args["summary_id"].(string)
	if !ok || summaryID == "" {
		return "", fmt.Errorf("memory describe: summary_id is required")
	}

	result, err := t.engine.Retrieval().Describe(ctx, summaryID)
	if err != nil {
		return "", fmt.Errorf("memory describe: %w", err)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory describe: marshal: %w", err)
	}
	return string(out), nil
}

func (t *MemoryTool) expand(ctx context.Context, args map[string]any) (string, error) {
	summaryID, ok := args["summary_id"].(string)
	if !ok || summaryID == "" {
		return "", fmt.Errorf("memory expand: summary_id is required")
	}

	tokenCap := intArg(args, "token_cap", 0)

	result, err := t.engine.Retrieval().Expand(ctx, summaryID, tokenCap)
	if err != nil {
		return "", fmt.Errorf("memory expand: %w", err)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory expand: marshal: %w", err)
	}
	return string(out), nil
}

func (t *MemoryTool) userMemoryUpdate(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("memory user_memory_update: content is required")
	}

	userID := memory.UserIDFromContext(ctx)
	if userID == 0 {
		return "", fmt.Errorf("memory user_memory_update: no user context")
	}
	agentID := memory.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", fmt.Errorf("memory user_memory_update: no agent context")
	}

	if t.memStore == nil {
		return "", fmt.Errorf("memory user_memory_update: user memory store not configured")
	}

	if err := t.memStore.Set(ctx, userID, agentID, content); err != nil {
		return "", fmt.Errorf("memory user_memory_update: %w", err)
	}
	return "User memory updated. Changes will appear in your system prompt at the next session start.", nil
}
