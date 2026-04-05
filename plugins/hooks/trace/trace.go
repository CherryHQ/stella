package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/pkg/hooks"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
)

func init() {
	pluginhooks.Register("trace", pluginhooks.Registration{
		Factory: func(_ pluginhooks.BuildContext) (hooks.HookPlugin, error) {
			return &Hook{log: slog.With("hook", "trace")}, nil
		},
	})
}

// Hook logs LLM and tool call details for debugging.
type Hook struct {
	log *slog.Logger
}

func (h *Hook) Name() string  { return "trace" }
func (h *Hook) Priority() int { return 0 } // runs first

// --- PreLLMCall ---

func (h *Hook) OnPreLLMCall(_ context.Context, hctx *hooks.PreLLMCallContext) (hooks.PreLLMCallResult, error) {
	tools := make([]string, len(hctx.ToolDefinitions))
	for i, t := range hctx.ToolDefinitions {
		tools[i] = t.Name
	}
	h.log.Info("pre_llm_call",
		"model", hctx.Model,
		"messages", hctx.MessageCount,
		"tools", len(tools),
		"tool_names", tools,
		"system_len", len(hctx.System),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	)
	return hooks.PreLLMCallResult{}, nil
}

// --- PostLLMCall ---

func (h *Hook) OnPostLLMCall(_ context.Context, hctx *hooks.PostLLMCallContext) {
	attrs := []any{
		"model", hctx.Model,
		"stop_reason", hctx.StopReason,
		"duration", hctx.Duration,
		"input_tokens", hctx.Usage.InputTokens,
		"output_tokens", hctx.Usage.OutputTokens,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.Info("post_llm_call", attrs...)
}

// --- PreToolCall ---

func (h *Hook) OnPreToolCall(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	input := summarizeArgs(hctx.ToolName, hctx.Arguments)
	h.log.Info("pre_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"input", input,
		"session_id", hctx.SessionID,
	)
	return hooks.PreToolCallResult{}, nil
}

// --- PostToolCall ---

func (h *Hook) OnPostToolCall(_ context.Context, hctx *hooks.PostToolCallContext) {
	resultSnippet := hctx.Result
	if len(resultSnippet) > 200 {
		resultSnippet = resultSnippet[:200] + "..."
	}
	h.log.Info("post_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"is_error", hctx.IsError,
		"duration", hctx.Duration,
		"result_len", len(hctx.Result),
		"result", resultSnippet,
		"session_id", hctx.SessionID,
	)
}

func summarizeArgs(tool string, args map[string]any) string {
	switch tool {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 200 {
				return cmd[:200] + "..."
			}
			return cmd
		}
	case "read", "write", "edit":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	s := string(data)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
