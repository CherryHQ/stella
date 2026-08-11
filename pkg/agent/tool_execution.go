package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

type toolCallLimitKey struct{}

type toolCallBudget struct {
	mu     sync.Mutex
	limits map[string]int
	used   map[string]int
}

func withToolCallLimits(ctx context.Context, limits map[string]int) context.Context {
	if _, ok := ctx.Value(toolCallLimitKey{}).(*toolCallBudget); ok || len(limits) == 0 {
		return ctx
	}
	return context.WithValue(ctx, toolCallLimitKey{}, &toolCallBudget{
		limits: limits,
		used:   make(map[string]int),
	})
}

func consumeToolCall(ctx context.Context, name string) (bool, int) {
	budget, _ := ctx.Value(toolCallLimitKey{}).(*toolCallBudget)
	if budget == nil || budget.limits[name] <= 0 {
		return true, 0
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	limit := budget.limits[name]
	if budget.used[name] >= limit {
		return false, limit
	}
	budget.used[name]++
	return true, limit
}

func refundToolCall(ctx context.Context, name string) {
	budget, _ := ctx.Value(toolCallLimitKey{}).(*toolCallBudget)
	if budget == nil {
		return
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.used[name] == 0 {
		return
	}
	budget.used[name]--
}

// toolCallbacks emits progress events around tool execution.
type toolCallbacks struct {
	onStart  func(call ai.ToolCall)
	onFinish func(result ai.ToolResultMessage)
}

// executeToolCalls runs each tool call in order and returns result messages.
func executeToolCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))
	appendFinal := func(result ai.ToolResultMessage) error {
		if canonicalize != nil {
			var err error
			result, err = canonicalize(ctx, result)
			if err != nil {
				return err
			}
		}
		results = append(results, result)
		if cb.onFinish != nil {
			cb.onFinish(result)
		}
		return nil
	}

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
			if err := appendFinal(result); err != nil {
				return nil, err
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
				if err := appendFinal(result); err != nil {
					return nil, err
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
		runPostToolCall := func(postCtx context.Context, postArgs map[string]any, resultText string, isError bool, duration time.Duration) {
			if hs.Empty() {
				return
			}
			// Only the first TextContent block is passed — sufficient for telemetry;
			// hooks needing full output should extend PostToolCallContext.
			hs.RunPostToolCall(postCtx, &hooks.PostToolCallContext{
				HookMeta:   meta,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  postArgs,
				Result:     resultText,
				IsError:    isError,
				Duration:   duration,
			})
		}
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
			if preResult.Arguments != nil {
				args = preResult.Arguments
			}
			if preResult.Block {
				blockMsg := preResult.BlockMessage
				if blockMsg == "" {
					blockMsg = "tool call blocked by hook"
				}
				runPostToolCall(execCtx, args, blockMsg, true, 0)
				result := ai.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Content:    []ai.ContentBlock{ai.TextContent{Text: blockMsg}},
				}
				if err := appendFinal(result); err != nil {
					return nil, err
				}
				continue
			}
		}

		// Execute tool with (possibly rewritten) args.
		if allowed, limit := consumeToolCall(ctx, call.Name); !allowed {
			message := fmt.Sprintf("tool call limit reached: %s may run at most %d times for this user message", call.Name, limit)
			runPostToolCall(execCtx, args, message, true, 0)
			result := ai.ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				IsError:    true,
				Content:    []ai.ContentBlock{ai.TextContent{Text: message}},
			}
			if err := appendFinal(result); err != nil {
				return nil, err
			}
			continue
		}
		execCall := call
		execCall.Arguments = args
		start := time.Now()
		toolCtx := pkgchannel.WithNotificationAgentID(execCtx, meta.AgentID)
		content, err := toolFn(toolCtx, execCall)
		duration := time.Since(start)
		if pkgtools.IsInvalidInput(err) {
			// Argument validation did not run the tool's operation. Return the
			// slot so the model still has the configured number of real attempts.
			refundToolCall(ctx, call.Name)
		}

		result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: content}
		if err != nil {
			result.IsError = true
			errText := err.Error()
			if t := ai.FlattenText(content); t != "" {
				errText = t + "\n" + errText
			}
			result.Content = []ai.ContentBlock{ai.TextContent{Text: errText}}
		}
		if len(result.Content) == 0 {
			result.Content = []ai.ContentBlock{ai.TextContent{Text: ""}}
		}

		resultText := ai.FlattenText(result.Content)

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
				runPostToolCall(execCtx, args, err.Error(), true, duration)
				return nil, err
			}
			if mutation.Result != nil {
				resultText = *mutation.Result
				result.Content = replaceTextContent(result.Content, resultText)
			}
			if mutation.IsError != nil {
				result.IsError = *mutation.IsError
			}
		}

		runPostToolCall(execCtx, args, resultText, result.IsError, duration)

		if err := appendFinal(result); err != nil {
			return nil, err
		}
	}

	if len(calls) > 0 && len(results) == 0 {
		return nil, errors.New("tool execution produced no results")
	}

	return results, nil
}

func replaceTextContent(blocks []ai.ContentBlock, text string) []ai.ContentBlock {
	out := make([]ai.ContentBlock, len(blocks))
	copy(out, blocks)
	for i, block := range out {
		if _, ok := block.(ai.TextContent); ok {
			out[i] = ai.TextContent{Text: text}
			return out
		}
	}
	return append([]ai.ContentBlock{ai.TextContent{Text: text}}, out...)
}
