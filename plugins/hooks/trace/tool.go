package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/pkg/hooks"
)

func (h *Hook) OnPreToolCall(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	input := summarizeArgs(hctx.ToolName, hctx.Arguments)
	h.log.Info("pre_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"input", input,
		"session_id", hctx.SessionID,
	)

	if h.otelEnabled() {
		h.mu.Lock()
		st := h.sessions[hctx.SessionID]
		h.mu.Unlock()
		if st != nil {
			parentCtx := st.turnCtx
			if parentCtx == nil {
				parentCtx = st.chatCtx
			}

			_, span := h.tracer.Start(parentCtx, "gen_ai.execute_tool",
				trace.WithAttributes(
					attribute.String("gen_ai.operation.name", "execute_tool"),
					attribute.String("gen_ai.tool.name", hctx.ToolName),
					attribute.String("gen_ai.tool.call.id", hctx.ToolCallID),
				),
			)

			h.mu.Lock()
			st.toolSpans[hctx.ToolCallID] = span
			st.activeOps.Add(1)
			st.lastActive = time.Now()
			h.mu.Unlock()
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
	)

	if !h.otelEnabled() {
		return
	}
	h.mu.Lock()
	st := h.sessions[hctx.SessionID]
	if st == nil {
		h.mu.Unlock()
		return
	}
	span, ok := st.toolSpans[hctx.ToolCallID]
	if ok {
		delete(st.toolSpans, hctx.ToolCallID)
	}
	h.mu.Unlock()

	if !ok || span == nil {
		return
	}

	if hctx.IsError {
		span.SetStatus(codes.Error, "tool execution failed")
		span.SetAttributes(attribute.String("error.type", "tool_error"))
	}
	span.End()

	st.activeOps.Add(-1)
	st.lastActive = time.Now()
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
