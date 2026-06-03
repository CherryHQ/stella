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

// ContentTool is an optional interface for tools whose result may include
// non-text content (e.g. images). Tools that only emit text need not implement
// it; ExecuteToolContent wraps their string output automatically.
type ContentTool interface {
	ExecuteContent(ctx context.Context, args map[string]any) ([]ai.ContentBlock, error)
}

// ExecuteToolContent runs a tool and returns its result as content blocks.
// Tools implementing ContentTool are called directly; others have their string
// output wrapped in a single TextContent block. Any partial text is preserved
// alongside an error so callers can surface it.
func ExecuteToolContent(ctx context.Context, t Tool, args map[string]any) ([]ai.ContentBlock, error) {
	if ct, ok := t.(ContentTool); ok {
		return ct.ExecuteContent(ctx, args)
	}
	text, err := t.Execute(ctx, args)
	return []ai.ContentBlock{ai.TextContent{Text: text}}, err
}
