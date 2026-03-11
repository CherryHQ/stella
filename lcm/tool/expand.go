package tool

import (
	"context"
	"encoding/json"
	"fmt"

	aitypes "github.com/vaayne/anna/ai/types"
	"github.com/vaayne/anna/lcm"
)

// ExpandTool drills into a summary to retrieve original details.
type ExpandTool struct {
	engine lcm.Engine
}

// NewExpandTool creates a memory_expand tool.
func NewExpandTool(engine lcm.Engine) *ExpandTool {
	return &ExpandTool{engine: engine}
}

func (t *ExpandTool) Definition() aitypes.ToolDefinition {
	return aitypes.ToolDefinition{
		Name:        "memory_expand",
		Description: "Drill into a summary to retrieve original messages (leaf) or child summaries (condensed). Use to recover details lost during compaction.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary_id": map[string]any{
					"type":        "string",
					"description": "The summary ID to expand.",
				},
				"token_cap": map[string]any{
					"type":        "integer",
					"description": "Maximum tokens of content to return (default 4000).",
				},
			},
			"required": []string{"summary_id"},
		},
	}
}

func (t *ExpandTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	summaryID, ok := args["summary_id"].(string)
	if !ok || summaryID == "" {
		return "", fmt.Errorf("memory_expand: summary_id is required")
	}

	tokenCap := intArg(args, "token_cap", 0)

	result, err := t.engine.Retrieval().Expand(ctx, summaryID, tokenCap)
	if err != nil {
		return "", fmt.Errorf("memory_expand: %w", err)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory_expand: marshal: %w", err)
	}
	return string(out), nil
}
