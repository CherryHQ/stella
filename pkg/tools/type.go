package tools

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
)

// Definition is a callable tool definition exposed to a model.
type Definition = ai.ToolDefinition

// Tool is a tool that can be executed by the Go runner.
type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, args map[string]any) (string, error)
}
