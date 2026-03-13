package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/internal/memory"
	"github.com/vaayne/anna/internal/toolspec"
)

// GrepTool searches conversation history using the memory retrieval engine.
type GrepTool struct {
	engine memory.Engine
}

// NewGrepTool creates a memory_grep tool.
func NewGrepTool(engine memory.Engine) *GrepTool {
	return &GrepTool{engine: engine}
}

func (t *GrepTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        "memory_grep",
		Description: "Search conversation history for messages and summaries matching a pattern. Use to recall earlier discussions, decisions, or code changes.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Text pattern to search for (case-insensitive substring match).",
				},
				"scope": map[string]any{
					"type":        "string",
					"description": `Where to search: "messages", "summaries", or "both" (default).`,
					"enum":        []string{memory.ScopeMessages, memory.ScopeSummaries, memory.ScopeBoth},
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default 20).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GrepTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("memory_grep: pattern is required")
	}

	scope, _ := args["scope"].(string)
	limit := intArg(args, "limit", 0)

	sessionID := memory.SessionIDFromContext(ctx)
	if sessionID == "" {
		return "", fmt.Errorf("memory_grep: no session context")
	}

	results, err := t.engine.Retrieval().GrepBySession(ctx, sessionID, pattern, scope, limit)
	if err != nil {
		return "", fmt.Errorf("memory_grep: %w", err)
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory_grep: marshal: %w", err)
	}
	return string(out), nil
}
