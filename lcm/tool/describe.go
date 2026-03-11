package tool

import (
	"context"
	"encoding/json"
	"fmt"

	aitypes "github.com/vaayne/anna/ai/types"
	"github.com/vaayne/anna/lcm"
)

// DescribeTool inspects summary metadata and lineage.
type DescribeTool struct {
	engine lcm.Engine
}

// NewDescribeTool creates a memory_describe tool.
func NewDescribeTool(engine lcm.Engine) *DescribeTool {
	return &DescribeTool{engine: engine}
}

func (t *DescribeTool) Definition() aitypes.ToolDefinition {
	return aitypes.ToolDefinition{
		Name:        "memory_describe",
		Description: "Inspect a summary's content, metadata, and lineage (parents/children). Use after memory_grep returns summary results to understand context.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary_id": map[string]any{
					"type":        "string",
					"description": "The summary ID to describe (e.g., \"sum_abc123def456\").",
				},
			},
			"required": []string{"summary_id"},
		},
	}
}

func (t *DescribeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	summaryID, ok := args["summary_id"].(string)
	if !ok || summaryID == "" {
		return "", fmt.Errorf("memory_describe: summary_id is required")
	}

	result, err := t.engine.Retrieval().Describe(ctx, summaryID)
	if err != nil {
		return "", fmt.Errorf("memory_describe: %w", err)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory_describe: marshal: %w", err)
	}
	return string(out), nil
}
