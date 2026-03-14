package engine

import (
	"context"
	"errors"

	"github.com/vaayne/anna/internal/ai"
)

// ToolCallbacks emits progress events around tool execution.
type ToolCallbacks struct {
	OnStart     func(call ai.ToolCall)
	OnFinish    func(result ai.ToolResultMessage)
	PluginHooks PluginHookRunner // optional plugin lifecycle hooks
}

// ExecuteToolCalls runs each tool call in order and returns result messages.
func ExecuteToolCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, cb ToolCallbacks) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))

	for _, call := range calls {
		if cb.OnStart != nil {
			cb.OnStart(call)
		}

		// Run before_tool_call plugin hooks. If a hook returns an error,
		// the tool call is blocked and the error is returned to the LLM.
		if cb.PluginHooks != nil {
			if err := cb.PluginHooks.RunHooks(ctx, "before_tool_call", beforeToolCallData{
				ToolName:  call.Name,
				Arguments: call.Arguments,
			}); err != nil {
				result := ai.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					IsError:    true,
					Content:    []ai.ContentBlock{ai.TextContent{Text: "blocked by plugin: " + err.Error()}},
				}
				results = append(results, result)
				if cb.OnFinish != nil {
					cb.OnFinish(result)
				}
				continue
			}
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
			// Preserve tool output (e.g. stdout/stderr) alongside the error.
			// Without this, the LLM only sees the error message and loses
			// context like "command not found" from stderr.
			errText := err.Error()
			if content.Text != "" {
				errText = content.Text + "\n" + errText
			}
			result.Content = []ai.ContentBlock{ai.TextContent{Text: errText}}
		}

		// Run after_tool_call plugin hooks (fire-and-forget).
		if cb.PluginHooks != nil {
			_ = cb.PluginHooks.RunHooks(ctx, "after_tool_call", afterToolCallData{
				ToolName: call.Name,
				Result:   content.Text,
				IsError:  err != nil,
			})
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

// beforeToolCallData is passed to before_tool_call hooks.
// Mirrors pkg/plugin.BeforeToolCallEvent without importing it.
type beforeToolCallData struct {
	ToolName  string
	Arguments map[string]any
}

// afterToolCallData is passed to after_tool_call hooks.
// Mirrors pkg/plugin.AfterToolCallEvent without importing it.
type afterToolCallData struct {
	ToolName string
	Result   string
	IsError  bool
}
