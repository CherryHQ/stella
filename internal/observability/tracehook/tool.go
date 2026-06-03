package tracehook

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/hooks"
)

// Credential shapes scrubbed out of tool input/result before they become span
// attributes. Tracing is always-on infra (gated only by the OTLP endpoint), so
// raw bash commands and tool results would otherwise leak tokens to a
// third-party backend. Best-effort, not a guarantee — see CR-004.
var (
	// Credentials embedded in URLs: scheme://user:pass@host.
	reURLCreds = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s:/@]+@`)
	// "Bearer <token>" (covers Authorization: Bearer ...).
	reBearer = regexp.MustCompile(`(?i)bearer\s+\S+`)
	// key/token/secret/password assignments, e.g. api_key=x, token: x.
	reSecretAssign = regexp.MustCompile(`(?i)([a-z0-9_-]*(?:api[_-]?key|secret|token|password|passwd|pwd))(\s*[:=]\s*)\S+`)
)

// redactSecrets masks credential-like substrings. It runs before truncation so
// a secret near the 200-char boundary is still masked on the retained prefix.
func redactSecrets(s string) string {
	s = reURLCreds.ReplaceAllString(s, "$1:[REDACTED]@")
	s = reBearer.ReplaceAllString(s, "Bearer [REDACTED]")
	s = reSecretAssign.ReplaceAllString(s, "$1$2[REDACTED]")
	return s
}

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

	if h.otelEnabled() && hctx.SessionID != "" {
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

			_, span := h.tracer().Start(parentCtx, "gen_ai.execute_tool",
				trace.WithAttributes(
					attribute.String("gen_ai.operation.name", "execute_tool"),
					attribute.String("gen_ai.tool.name", hctx.ToolName),
					attribute.String("gen_ai.tool.call.id", hctx.ToolCallID),
					attribute.String("user_id", hctx.UserID),
					attribute.String("agent_id", hctx.AgentID),
					attribute.Int("gen_ai.tool.argument_count", len(hctx.Arguments)),
					attribute.String("gen_ai.tool.input", redactSecrets(input)),
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

	spanResult := redactSecrets(hctx.Result)
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
