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

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// Credential shapes scrubbed out of tool input/result before they become span
// attributes. Tracing is always-on infra (gated only by the OTLP endpoint), so
// raw bash commands and tool results would otherwise leak tokens to a
// third-party backend. Best-effort, not a guarantee — see CR-004.
var (
	// Credentials embedded in URLs: scheme://user:pass@host.
	reURLCreds = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s:/@]+@`)
	// HTTP auth schemes: "Bearer <token>", "Basic <b64>".
	reAuthScheme = regexp.MustCompile(`(?i)\b(bearer|basic)\s+\S+`)
	// Cookie/Set-Cookie header values (whole value is sensitive).
	reCookie = regexp.MustCompile(`(?i)(set-)?cookie(["']?\s*[:=]\s*)["']?[^\r\n"']+`)
	// key/token/secret/password assignments, e.g. api_key=x, "token":"x".
	// The separator tolerates surrounding quotes so JSON like {"api_key":"x"}
	// is also matched, not just shell-style key=value.
	reSecretAssign = regexp.MustCompile(`(?i)([a-z0-9_-]*(?:api[_-]?key|secret|token|password|passwd|pwd))(["']?\s*[:=]\s*["']?)\S+`)
	// Bare provider tokens with a recognizable prefix, e.g. sk-..., ghp_...,
	// xoxb-..., that may appear without an enclosing key.
	reBareToken = regexp.MustCompile(`(?i)\b(sk|pk|rk|ghp|gho|ghs|xox[bpas])[-_][A-Za-z0-9_-]{12,}`)
)

// redactSecrets masks credential-like substrings. Best-effort regex blacklist:
// it shrinks the leak surface but is not a guarantee — structured or novel
// secret shapes can still slip through (see CR-004). It runs before truncation
// so a secret near the 200-char boundary is still masked on the retained prefix.
func redactSecrets(s string) string {
	s = reURLCreds.ReplaceAllString(s, "$1:[REDACTED]@")
	s = reCookie.ReplaceAllString(s, "${1}cookie$2[REDACTED]")
	s = reAuthScheme.ReplaceAllString(s, "$1 [REDACTED]")
	s = reSecretAssign.ReplaceAllString(s, "$1$2[REDACTED]")
	s = reBareToken.ReplaceAllString(s, "[REDACTED]")
	return s
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

			action, _ := hctx.Arguments["action"].(string)
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
		if kind != ai.ToolErrorKindCommandNonzero {
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
