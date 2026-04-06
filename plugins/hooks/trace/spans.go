package trace

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/pkg/hooks"
)

// --- Hook interface methods (slog + OTel) ---

func (h *Hook) OnPreLLMCall(_ context.Context, hctx *hooks.PreLLMCallContext) (hooks.PreLLMCallResult, error) {
	h.logPreLLMCall(hctx)

	if h.otelEnabled() {
		h.mu.Lock()
		st := h.getOrCreateSession(hctx.SessionID)

		// Rotate turn: end previous turn span.
		if st.turnSpan != nil {
			st.turnSpan.End()
		}
		st.turnNum++
		st.turnCtx, st.turnSpan = h.tracer.Start(st.chatCtx,
			fmt.Sprintf("turn %d", st.turnNum),
		)

		// Start LLM call span as child of turn.
		_, st.llmSpan = h.tracer.Start(st.turnCtx, "gen_ai.chat",
			trace.WithAttributes(
				attribute.String("gen_ai.operation.name", "chat"),
				attribute.String("gen_ai.request.model", hctx.Model),
				attribute.String("gen_ai.conversation.id", hctx.SessionID),
			),
		)
		st.activeOps.Add(1)
		st.lastActive = time.Now()
		h.mu.Unlock()
	}

	return hooks.PreLLMCallResult{}, nil
}

func (h *Hook) OnPostLLMCall(_ context.Context, hctx *hooks.PostLLMCallContext) {
	h.logPostLLMCall(hctx)

	if !h.otelEnabled() {
		return
	}
	h.mu.Lock()
	st := h.sessions[hctx.SessionID]
	h.mu.Unlock()
	if st == nil || st.llmSpan == nil {
		return
	}

	span := st.llmSpan
	span.SetAttributes(
		attribute.String("gen_ai.provider.name", hctx.Provider),
		attribute.String("gen_ai.response.model", hctx.Model),
		attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(hctx.StopReason)}),
		attribute.Int("gen_ai.usage.input_tokens", hctx.Usage.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", hctx.Usage.OutputTokens),
	)
	if hctx.Usage.CacheRead > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_read.input_tokens", hctx.Usage.CacheRead))
	}
	if hctx.Usage.CacheWrite > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_creation.input_tokens", hctx.Usage.CacheWrite))
	}
	if hctx.TimeToFirstToken > 0 {
		span.SetAttributes(attribute.Float64("gen_ai.server.time_to_first_token", hctx.TimeToFirstToken.Seconds()))
	}
	if hctx.Error != nil {
		span.RecordError(hctx.Error)
		span.SetStatus(codes.Error, hctx.Error.Error())
		span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", hctx.Error)))
	}
	span.End()

	st.llmSpan = nil
	st.activeOps.Add(-1)
	st.lastActive = time.Now()
}

func (h *Hook) OnPreToolCall(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	h.logPreToolCall(hctx)

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
	h.logPostToolCall(hctx)

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

func (h *Hook) OnPostMemoryCall(_ context.Context, hctx *hooks.PostMemoryCallContext) {
	h.logPostMemoryCall(hctx)

	// Memory has post-hook only, so backdate the span.
	// Skip ops with no session ID (e.g. ListInfo is a global query).
	if !h.otelEnabled() || hctx.SessionID == "" {
		return
	}

	h.mu.Lock()
	st := h.getOrCreateSession(hctx.SessionID)
	h.mu.Unlock()

	parentCtx := st.chatCtx
	if st.turnCtx != nil {
		parentCtx = st.turnCtx
	}

	startTime := time.Now().Add(-hctx.Duration)
	_, span := h.tracer.Start(parentCtx, fmt.Sprintf("memory.%s", hctx.Op),
		trace.WithTimestamp(startTime),
		trace.WithAttributes(
			attribute.String("anna.memory.op", string(hctx.Op)),
			attribute.String("anna.memory.session_id", hctx.SessionID),
		),
	)
	if hctx.TokenCount > 0 {
		span.SetAttributes(attribute.Int("anna.memory.token_count", hctx.TokenCount))
	}
	if hctx.TokenDelta != 0 {
		span.SetAttributes(attribute.Int("anna.memory.token_delta", hctx.TokenDelta))
	}
	if hctx.MessageCount > 0 {
		span.SetAttributes(attribute.Int("anna.memory.message_count", hctx.MessageCount))
	}
	if hctx.Error != nil {
		span.RecordError(hctx.Error)
		span.SetStatus(codes.Error, hctx.Error.Error())
	}
	span.End(trace.WithTimestamp(time.Now()))
	st.lastActive = time.Now()
}
