package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/vaayne/anna/pkg/hooks"
)

// levelTrace is below slog.LevelDebug. When enabled, memory traces include
// the full Detail field (message content, search results, profile text).
const levelTrace = slog.Level(-8)

func (h *Hook) logPreLLMCall(hctx *hooks.PreLLMCallContext) {
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
}

func (h *Hook) logPostLLMCall(hctx *hooks.PostLLMCallContext) {
	attrs := []any{
		"provider", hctx.Provider,
		"model", hctx.Model,
		"stop_reason", hctx.StopReason,
		"duration", hctx.Duration.Round(time.Millisecond),
		"ttft", hctx.TimeToFirstToken.Round(time.Millisecond),
		"input_tokens", hctx.Usage.InputTokens,
		"output_tokens", hctx.Usage.OutputTokens,
		"cache_read", hctx.Usage.CacheRead,
		"cache_write", hctx.Usage.CacheWrite,
		"total_tokens", hctx.Usage.TotalTokens,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.Info("post_llm_call", attrs...)
}

func (h *Hook) logPreToolCall(hctx *hooks.PreToolCallContext) {
	input := summarizeArgs(hctx.ToolName, hctx.Arguments)
	h.log.Info("pre_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"input", input,
		"session_id", hctx.SessionID,
	)
}

func (h *Hook) logPostToolCall(hctx *hooks.PostToolCallContext) {
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

func (h *Hook) logPostMemoryCall(hctx *hooks.PostMemoryCallContext) {
	attrs := []any{
		"op", string(hctx.Op),
		"duration", hctx.Duration.Round(time.Millisecond),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	}
	if hctx.MessageCount > 0 {
		attrs = append(attrs, "message_count", hctx.MessageCount)
	}
	if hctx.TokenCount > 0 {
		attrs = append(attrs, "token_count", hctx.TokenCount)
	}
	if hctx.TokenDelta != 0 {
		attrs = append(attrs, "token_delta", hctx.TokenDelta)
	}
	if hctx.SummaryCount > 0 {
		attrs = append(attrs, "summary_count", hctx.SummaryCount)
	}
	if hctx.ResultCount > 0 {
		attrs = append(attrs, "result_count", hctx.ResultCount)
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	if hctx.Detail != "" && h.log.Enabled(context.Background(), levelTrace) {
		attrs = append(attrs, "detail", hctx.Detail)
	}
	h.log.Info("post_memory_call", attrs...)
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
