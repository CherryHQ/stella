package agent

import (
	"context"
	"errors"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// toolCallbacks emits progress events around tool execution.
type toolCallbacks struct {
	onStart  func(call ai.ToolCall)
	onFinish func(result ai.ToolResultMessage)
}

// executeToolCalls runs each tool call in order and returns result messages.
func executeToolCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))

	for _, call := range calls {
		if cb.onStart != nil {
			cb.onStart(call)
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
			if cb.onFinish != nil {
				cb.onFinish(result)
			}
			continue
		}

		args := call.Arguments
		if lifecycle != nil && lifecycle.BeforeCall != nil {
			mutation, err := lifecycle.BeforeCall(ctx, ToolCallContext{
				SessionID:  meta.SessionID,
				Channel:    meta.Channel,
				UserID:     meta.UserID,
				AgentID:    meta.AgentID,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  cloneArgs(args),
			})
			if err != nil {
				return nil, err
			}
			if mutation.Block {
				blockMsg := mutation.BlockMessage
				if blockMsg == "" {
					blockMsg = "tool call blocked by lifecycle"
				}
				result := ai.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Content:    []ai.ContentBlock{ai.TextContent{Text: blockMsg}},
				}
				results = append(results, result)
				if cb.onFinish != nil {
					cb.onFinish(result)
				}
				continue
			}
			if mutation.Arguments != nil {
				args = mutation.Arguments
			}
		}

		// PreToolCall hooks: may rewrite args, block execution, or enrich the
		// context (e.g. with a trace span the tool's DB calls nest under).
		execCtx := ctx
		if !hs.Empty() {
			preCtx := &hooks.PreToolCallContext{
				HookMeta:   meta,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  args,
			}
			preResult, _ := hs.RunPreToolCall(ctx, preCtx)
			if preResult.Context != nil {
				execCtx = preResult.Context
			}
			if preResult.Block {
				blockMsg := preResult.BlockMessage
				if blockMsg == "" {
					blockMsg = "tool call blocked by hook"
				}
				result := ai.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Content:    []ai.ContentBlock{ai.TextContent{Text: blockMsg}},
				}
				results = append(results, result)
				if cb.onFinish != nil {
					cb.onFinish(result)
				}
				continue
			}
			if preResult.Arguments != nil {
				args = preResult.Arguments
			}
		}

		// Execute tool with (possibly rewritten) args.
		execCall := call
		execCall.Arguments = args
		start := time.Now()
		toolCtx := pkgchannel.WithNotificationAgentID(execCtx, meta.AgentID)
		content, err := toolFn(toolCtx, execCall)
		duration := time.Since(start)

		result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: []ai.ContentBlock{content}}
		if err != nil {
			result.IsError = true
			errText := err.Error()
			if content.Text != "" {
				errText = content.Text + "\n" + errText
			}
			result.Content = []ai.ContentBlock{ai.TextContent{Text: errText}}
		}

		resultText := ""
		for _, block := range result.Content {
			if tc, ok := block.(ai.TextContent); ok {
				resultText = tc.Text
				break
			}
		}

		if lifecycle != nil && lifecycle.AfterCall != nil {
			mutation, err := lifecycle.AfterCall(ctx, ToolResultContext{
				SessionID:  meta.SessionID,
				Channel:    meta.Channel,
				UserID:     meta.UserID,
				AgentID:    meta.AgentID,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  cloneArgs(args),
				Result:     resultText,
				IsError:    result.IsError,
				Duration:   duration,
			})
			if err != nil {
				return nil, err
			}
			if mutation.Result != nil {
				resultText = *mutation.Result
				result.Content = []ai.ContentBlock{ai.TextContent{Text: resultText}}
			}
			if mutation.IsError != nil {
				result.IsError = *mutation.IsError
			}
		}

		// PostToolCall hooks: observe results.
		// Only the first TextContent block is passed — sufficient for telemetry;
		// hooks needing full output should extend PostToolCallContext.
		if !hs.Empty() {
			postCtx := &hooks.PostToolCallContext{
				HookMeta:   meta,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  args,
				Result:     resultText,
				IsError:    result.IsError,
				Duration:   duration,
			}
			hs.RunPostToolCall(ctx, postCtx)
		}

		results = append(results, result)
		if cb.onFinish != nil {
			cb.onFinish(result)
		}
	}

	if len(calls) > 0 && len(results) == 0 {
		return nil, errors.New("tool execution produced no results")
	}

	return results, nil
}
