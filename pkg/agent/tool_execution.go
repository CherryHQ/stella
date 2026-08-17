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

// runtimeHookContext keeps the hook's cancellation/deadline and values while
// falling back to the immutable runtime values (AgentRun guard, authority,
// Session identity). A hook may return an independent context; it must not
// accidentally strip the durable execution fence from a model or tool call.
type runtimeHookContext struct {
	context.Context
	fallback context.Context
}

func (c runtimeHookContext) Value(key any) any {
	if value := c.Context.Value(key); value != nil {
		return value
	}
	return c.fallback.Value(key)
}

// executeToolCalls runs each tool call in order and returns result messages.
func executeToolCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ([]ai.ToolResultMessage, error) {
	results := make([]ai.ToolResultMessage, 0, len(calls))
	checkOperation := func(checkCtx context.Context) error {
		if lifecycle == nil || lifecycle.OperationCheck == nil {
			return nil
		}
		return lifecycle.OperationCheck(checkCtx)
	}
	appendFinal := func(result ai.ToolResultMessage) error {
		if canonicalize != nil {
			if err := checkOperation(ctx); err != nil {
				return err
			}
			var err error
			result, err = canonicalize(ctx, result)
			if err != nil {
				return err
			}
			if err := checkOperation(ctx); err != nil {
				return err
			}
		}
		results = append(results, result)
		if cb.onFinish != nil {
			if err := checkOperation(ctx); err != nil {
				return err
			}
			cb.onFinish(result)
		}
		return nil
	}

	for _, call := range calls {
		if cb.onStart != nil {
			if err := checkOperation(ctx); err != nil {
				return nil, err
			}
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
		if err := checkOperation(ctx); err != nil {
			return nil, err
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
			if err := checkOperation(ctx); err != nil {
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
		runPostToolCall := func(postCtx context.Context, postArgs map[string]any, resultText string, isError bool, duration time.Duration) error {
			if hs.Empty() {
				return nil
			}
			if err := checkOperation(ctx); err != nil {
				return err
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
			return checkOperation(ctx)
		}
		if !hs.Empty() {
			preCtx := &hooks.PreToolCallContext{
				HookMeta:   meta,
				ToolName:   call.Name,
				ToolCallID: call.ID,
				Arguments:  args,
			}
			preResult, err := hs.RunPreToolCall(ctx, preCtx)
			if err != nil {
				return nil, err
			}
			if err := checkOperation(ctx); err != nil {
				return nil, err
			}
			if preResult.Context != nil {
				execCtx = runtimeHookContext{Context: preResult.Context, fallback: ctx}
			}
			if preResult.Arguments != nil {
				args = preResult.Arguments
			}
			if preResult.Block {
				blockMsg := preResult.BlockMessage
				if blockMsg == "" {
					blockMsg = "tool call blocked by hook"
				}
				if err := runPostToolCall(execCtx, args, blockMsg, true, 0); err != nil {
					return nil, err
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
		}

		// Execute tool with (possibly rewritten) args.
		execCall := call
		execCall.Arguments = args
		// Lifecycle and hook processing may block. Revalidate at the actual
		// external-effect boundary rather than relying on the earlier admission
		// check for this tool call.
		if err := checkOperation(ctx); err != nil {
			return nil, err
		}
		start := time.Now()
		toolCtx := pkgchannel.WithNotificationAgentID(execCtx, meta.AgentID)
		content, err := toolFn(toolCtx, execCall)
		duration := time.Since(start)
		// The tool may have completed after this executor lost ownership. Its
		// outcome is then unknown: discard it and stop rather than feeding a stale
		// result into another model turn or source-domain write.
		if err := checkOperation(ctx); err != nil {
			return nil, err
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
				if hookErr := runPostToolCall(execCtx, args, err.Error(), true, duration); hookErr != nil {
					return nil, hookErr
				}
				return nil, err
			}
			if err := checkOperation(ctx); err != nil {
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

		if err := runPostToolCall(execCtx, args, resultText, result.IsError, duration); err != nil {
			return nil, err
		}

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
