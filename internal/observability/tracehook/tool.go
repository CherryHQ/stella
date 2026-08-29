package tracehook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// redactSecrets masks credential-like substrings. Best-effort regex blacklist:
// it shrinks the leak surface but is not a guarantee — structured or novel
// secret shapes can still slip through (see CR-004). It runs before truncation
// so a secret near the 200-char boundary is still masked on the retained prefix.
func redactSecrets(s string) string {
	return hooks.RedactToolText(s)
}

// recordSpanError marks span as failed without exporting the error message.
// Provider/LLM/memory errors routinely embed upstream HTTP bodies, tokens,
// request URLs, or prompt fragments, and a span status is exported off-box.
// Redaction was tried and is not enough: a blacklist cannot know every
// credential-shaped query parameter a gateway invents. So only two things
// leave the process — the concrete Go type, which is a closed set written by
// us, and a fixed description. The message itself stays in the logs, which
// are not exported by default.
func recordSpanError(span trace.Span, err error, what string) {
	span.SetStatus(codes.Error, what)
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}

func (h *Hook) OnPreToolCall(ctx context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	input := redactSecrets(summarizeArgs(hctx.ToolName, hctx.Arguments))
	h.log.InfoContext(ctx, "pre_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"input", input,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
	)

	if h.otelEnabled() && hctx.SessionID != "" {
		key := sessionKey(hctx.AgentID, hctx.SessionID)
		h.mu.Lock()
		st := h.sessions[key]
		h.mu.Unlock()
		if st != nil {
			st.mu.Lock()
			parentCtx := st.turnCtx
			if parentCtx == nil {
				parentCtx = st.loopCtx
			}

			action := h.toolAction(hctx.ToolName, hctx.Arguments)
			attrs := []attribute.KeyValue{
				attribute.String("gen_ai.operation.name", "execute_tool"),
				attribute.String("gen_ai.tool.name", hctx.ToolName),
				attribute.String("gen_ai.tool.call.id", hctx.ToolCallID),
				attribute.String("tool", hctx.ToolName),
				attribute.String("action", action),
				attribute.String("user_id", hctx.UserID),
				attribute.String("agent_id", hctx.AgentID),
				attribute.Int("gen_ai.tool.argument_count", len(hctx.Arguments)),
			}
			if h.recordIO {
				attrs = append(attrs, attribute.String("gen_ai.tool.input", input))
			}
			toolCtx, span := h.tracer().Start(parentCtx, "gen_ai.execute_tool",
				trace.WithAttributes(attrs...),
			)

			st.toolSpans[hctx.ToolCallID] = span
			st.lastActive = time.Now()
			st.mu.Unlock()
			st.activeOps.Add(1)

			// Hand the span-enriched context back so the tool's DB/memory work
			// nests under this tool span instead of becoming root spans.
			return hooks.PreToolCallResult{Context: toolCtx}, nil
		}
	}

	return hooks.PreToolCallResult{}, nil
}

func (h *Hook) OnPostToolCall(ctx context.Context, hctx *hooks.PostToolCallContext) {
	resultSnippet := redactSecrets(hctx.Result)
	if len(resultSnippet) > 200 {
		resultSnippet = resultSnippet[:200] + "..."
	}
	logAttrs := []any{
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"is_error", hctx.IsError,
		"duration", hctx.Duration,
		"result_len", len(hctx.Result),
		"result", resultSnippet,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
	}
	if hctx.ErrorKind != "" {
		logAttrs = append(logAttrs, "error_kind", string(hctx.ErrorKind))
	}
	if hctx.ExitCode != nil {
		logAttrs = append(logAttrs, "exit_code", *hctx.ExitCode)
	}
	h.log.InfoContext(ctx, "post_tool_call", logAttrs...)

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

	span.SetAttributes(
		attribute.Int("gen_ai.tool.result_len", len(hctx.Result)),
		attribute.Float64("gen_ai.tool.duration_s", hctx.Duration.Seconds()),
	)
	if h.recordIO {
		span.SetAttributes(attribute.String("gen_ai.tool.result", resultSnippet))
	}
	if hctx.ExitCode != nil {
		span.SetAttributes(attribute.Int("gen_ai.tool.exit_code", *hctx.ExitCode))
	}
	if hctx.IsError {
		kind := hctx.ErrorKind
		if kind == "" {
			// Pre-#1077 callers pass no kind; the default failure is the tool.
			kind = ai.ToolErrorKindTool
		}
		span.SetAttributes(
			attribute.String("gen_ai.tool.error_kind", string(kind)),
			attribute.String("error.type", string(kind)),
		)
		// Only a broken tool is a failed operation. A command that ran and
		// exited nonzero is the sandbox answering (#1077) — marking it Error
		// would report normal exploration as breakage in every error-rate view.
		if kind != ai.ToolErrorKindCommandNonzero && kind != ai.ToolErrorKindCommandTimeout {
			span.SetStatus(codes.Error, "tool execution failed")
		}
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

// toolAction reports the action a call performed. A split tool carries it in
// the name, so the attribute stays comparable across the split instead of going
// empty the moment a union became one tool per action; the unions that have not
// been split yet still carry it as an argument.
func (h *Hook) toolAction(name string, args map[string]any) string {
	if action := h.toolMeta.Action(name); action != "" {
		return action
	}
	action, _ := args["action"].(string)
	return action
}
