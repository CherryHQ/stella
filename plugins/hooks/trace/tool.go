package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/hooks"
)

func (h *Hook) OnPreToolCall(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	input := summarizeArgs(hctx.ToolName, hctx.Arguments)
	h.log.Info("pre_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"input", input,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
	)

	if h.otelEnabled() {
		key := sessionKey(hctx.AgentID, hctx.SessionID)
		h.mu.Lock()
		st := h.sessions[key]
		h.mu.Unlock()
		if st != nil {
			st.mu.Lock()
			parentCtx := st.turnCtx
			if parentCtx == nil {
				parentCtx = st.chatCtx
			}

			_, span := h.tracer.Start(parentCtx, "gen_ai.execute_tool",
				trace.WithAttributes(
					attribute.String("gen_ai.operation.name", "execute_tool"),
					attribute.String("gen_ai.tool.name", hctx.ToolName),
					attribute.String("gen_ai.tool.call.id", hctx.ToolCallID),
					attribute.String("user_id", hctx.UserID),
					attribute.String("agent_id", hctx.AgentID),
					attribute.Int("gen_ai.tool.argument_count", len(hctx.Arguments)),
					attribute.String("gen_ai.tool.input", input),
				),
			)

			st.toolSpans[hctx.ToolCallID] = span
			st.lastActive = time.Now()
			st.mu.Unlock()
			st.activeOps.Add(1)
		}
	}

	return hooks.PreToolCallResult{}, nil
}

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
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
	)

	if !h.otelEnabled() {
		return
	}
	key := sessionKey(hctx.AgentID, hctx.SessionID)
	h.mu.Lock()
	st := h.sessions[key]
	h.mu.Unlock()
	if st == nil {
		return
	}

	st.mu.Lock()
	span, ok := st.toolSpans[hctx.ToolCallID]
	if ok {
		delete(st.toolSpans, hctx.ToolCallID)
	}
	st.mu.Unlock()

	if !ok || span == nil {
		return
	}

	spanResult := hctx.Result
	if len(spanResult) > 200 {
		spanResult = spanResult[:200] + "..."
	}
	span.SetAttributes(
		attribute.Int("gen_ai.tool.result_len", len(hctx.Result)),
		attribute.String("gen_ai.tool.result", spanResult),
		attribute.Float64("gen_ai.tool.duration_s", hctx.Duration.Seconds()),
	)
	if hctx.IsError {
		span.SetStatus(codes.Error, "tool execution failed")
		span.SetAttributes(attribute.String("error.type", "tool_error"))
	}
	span.End()

	st.activeOps.Add(-1)
	st.mu.Lock()
	st.lastActive = time.Now()
	st.mu.Unlock()
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
		if path, ok := args["path"].(string); ok {
			return path
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
