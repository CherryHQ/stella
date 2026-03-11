package engine

import (
	"context"
	"errors"

	"github.com/vaayne/anna/ai"
)

// ToolCallbacks emits progress events around tool execution.
type ToolCallbacks struct {
	OnStart  func(call ai.ToolCall)
	OnFinish func(result ai.ToolResultMessage)
}

// ExecuteToolCalls runs each tool call in order and returns result messages.
func ExecuteToolCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, cb ToolCallbacks) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))

	for _, call := range calls {
		if cb.OnStart != nil {
			cb.OnStart(call)
		}

		toolFn, ok := tools[call.Name]
		if !ok {
			result := ai.ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				IsError:    true,
				Content:    []ai.ContentBlock{ai.TextContent{Text: "tool not found"}},
			}
			results = append(results, result)
			if cb.OnFinish != nil {
				cb.OnFinish(result)
			}
			continue
		}

		content, err := toolFn(ctx, call)
		result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: []ai.ContentBlock{content}}
		if err != nil {
			result.IsError = true
			result.Content = []ai.ContentBlock{ai.TextContent{Text: err.Error()}}
		}
		results = append(results, result)
		if cb.OnFinish != nil {
			cb.OnFinish(result)
		}
	}

	if len(calls) > 0 && len(results) == 0 {
		return nil, errors.New("tool execution produced no results")
	}

	return results, nil
}
