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
		result = NormalizeToolResult(result)
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
				ErrorKind:  ai.ToolErrorKindTool,
				Content:    []ai.ContentBlock{ai.TextContent{Text: "tool not found"}},
			}
			if err := appendFinal(result); err != nil {
				return results, err
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
				return results, err
			}
			if mutation.Block {
				blockMsg := mutation.BlockMessage
				if blockMsg == "" {
					blockMsg = "tool call blocked by lifecycle"
				}
				result := ai.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					IsError:    true,
					Content:    []ai.ContentBlock{ai.TextContent{Text: blockMsg}},
				}
				if err := appendFinal(result); err != nil {
					return results, err
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
		runPostToolCall := func(postCtx context.Context, postArgs map[string]any, resultText string, isError bool, kind ai.ToolErrorKind, exitCode *int, duration time.Duration) {
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
				ErrorKind:  kind,
				ExitCode:   exitCode,
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
				runPostToolCall(execCtx, args, blockMsg, true, "", nil, 0)
				result := ai.ToolResultMessage{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					IsError:    true,
					Content:    []ai.ContentBlock{ai.TextContent{Text: blockMsg}},
				}
				if err := appendFinal(result); err != nil {
					return results, err
				}
				continue
			}
		}

		// Execute tool with (possibly rewritten) args.
		execCall := call
		execCall.Arguments = args
		start := time.Now()
		toolCtx := pkgchannel.WithNotificationAgentID(execCtx, meta.AgentID)
		content, err := toolFn(toolCtx, execCall)
		duration := time.Since(start)
		result := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: content}
		var exitCode *int
		if err != nil {
			result.IsError = true
			result.ErrorKind, exitCode = classifyToolError(err)
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
				runPostToolCall(execCtx, args, err.Error(), true, ai.ToolErrorKindTool, nil, duration)
				return results, err
			}
			if mutation.Result != nil {
				resultText = *mutation.Result
				result.Content = replaceTextContent(result.Content, resultText)
			}
			if mutation.IsError != nil {
				result.IsError = *mutation.IsError
				// The lifecycle overrode the verdict, so the original reason no
				// longer describes this result. An error it declared is a tool
				// error; a success carries no kind.
				result.ErrorKind = ""
				exitCode = nil
				if result.IsError {
					result.ErrorKind = ai.ToolErrorKindTool
				}
			}
		}

		runPostToolCall(execCtx, args, resultText, result.IsError, result.ErrorKind, exitCode, duration)

		if err := appendFinal(result); err != nil {
			return results, err
		}
	}

	if len(calls) > 0 && len(results) == 0 {
		return nil, errors.New("tool execution produced no results")
	}

	return results, nil
}

// classifyToolError names the failure so downstream consumers never have to
// read it out of the message text. A command that ran and exited nonzero is
// the sandbox answering; everything else is the tool failing. The exit code
// comes back only with the former, where it exists.
func classifyToolError(err error) (ai.ToolErrorKind, *int) {
	var timeoutErr *ai.CommandTimeoutError
	if errors.As(err, &timeoutErr) {
		code := -1
		return ai.ToolErrorKindCommandTimeout, &code
	}
	var exitErr *ai.CommandExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode
		return ai.ToolErrorKindCommandNonzero, &code
	}
	return ai.ToolErrorKindTool, nil
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
