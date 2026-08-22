package tracehook

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/hooks"
)

func (h *Hook) OnPreLLMCall(ctx context.Context, hctx *hooks.PreLLMCallContext) (hooks.PreLLMCallResult, error) {
	tools := make([]string, len(hctx.ToolDefinitions))
	for i, t := range hctx.ToolDefinitions {
		tools[i] = t.Name
	}
	h.log.InfoContext(ctx, "pre_llm_call",
		"model", hctx.Model,
		"messages", hctx.MessageCount,
		"tools", len(tools),
		"tool_names", tools,
		"system_len", len(hctx.System),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
	)

	if h.otelEnabled() && hctx.SessionID != "" {
		h.mu.Lock()
		st := h.getOrCreateSession(ctx, hctx.AgentID, hctx.SessionID)
		h.mu.Unlock()

		st.mu.Lock()
		// End a prior LLM span that never saw its OnPostLLMCall (e.g. a dropped
		// or interleaved call) so it can't leak or pin activeOps forever.
		if st.llmSpan != nil {
			st.llmSpan.End()
			st.llmSpan = nil
			st.activeOps.Add(-1)
		}
		if st.turnSpan != nil {
			st.turnSpan.End()
		}
		st.turnNum++
		st.turnCtx, st.turnSpan = h.tracer().Start(st.loopCtx,
			fmt.Sprintf("turn %d", st.turnNum),
			trace.WithAttributes(
				attribute.Int("stella.turn.number", st.turnNum),
				attribute.String("user_id", hctx.UserID),
				attribute.String("agent_id", hctx.AgentID),
			),
		)

		attrs := []attribute.KeyValue{
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", hctx.API),
			attribute.String("gen_ai.request.model", hctx.Model),
			attribute.String("gen_ai.conversation.id", hctx.SessionID),
			attribute.String("user_id", hctx.UserID),
			attribute.String("agent_id", hctx.AgentID),
			attribute.Int("gen_ai.request.message_count", hctx.MessageCount),
			attribute.Int("gen_ai.request.tool_count", len(tools)),
			attribute.StringSlice("gen_ai.request.tool_names", tools),
			attribute.Int("gen_ai.request.system_prompt_len", len(hctx.System)),
		}
		if hctx.Provider != "" {
			attrs = append(attrs, attribute.String("gen_ai.provider.name", hctx.Provider))
		}
		if hctx.BaseURL != "" {
			attrs = append(attrs, attribute.String("server.address", hctx.BaseURL))
		}
		if hctx.MaxTokens != nil {
			attrs = append(attrs, attribute.Int("gen_ai.request.max_tokens", *hctx.MaxTokens))
		}
		if hctx.Temperature != nil {
			attrs = append(attrs, attribute.Float64("gen_ai.request.temperature", *hctx.Temperature))
		}

		var llmCtx context.Context
		llmCtx, st.llmSpan = h.tracer().Start(st.turnCtx, "gen_ai.chat",
			trace.WithAttributes(attrs...),
		)
		st.activeOps.Add(1)
		st.lastActive = time.Now()
		st.mu.Unlock()

		// Return the span context so the provider HTTP call becomes a child of gen_ai.chat.
		return hooks.PreLLMCallResult{Context: llmCtx}, nil
	}

	return hooks.PreLLMCallResult{}, nil
}

func (h *Hook) OnPostLLMCall(ctx context.Context, hctx *hooks.PostLLMCallContext) {
	attrs := []any{
		"provider", hctx.Provider,
		"model", hctx.Model,
		"stop_reason", hctx.StopReason,
		"duration", hctx.Duration.Round(time.Millisecond),
		"ttft", hctx.TimeToFirstToken.Round(time.Millisecond),
		"attempts", hctx.Attempts,
		"input_tokens", hctx.Usage.InputTokens,
		"output_tokens", hctx.Usage.OutputTokens,
		"cache_read", hctx.Usage.CacheRead,
		"cache_write", hctx.Usage.CacheWrite,
		"total_tokens", hctx.Usage.TotalTokens,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
	}
	if hctx.Usage.Cost.Total > 0 {
		attrs = append(attrs, "cost_usd", hctx.Usage.Cost.Total)
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.InfoContext(ctx, "post_llm_call", attrs...)

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

	// Claim the span under the lock (nil it out) before ending it, so a
	// concurrent endSession (reaper/Close) can never snapshot the same span and
	// End it twice. Mirrors OnPostToolCall, which deletes from the map first.
	st.mu.Lock()
	span := st.llmSpan
	st.llmSpan = nil
	if span != nil {
		st.lastActive = time.Now()
	}
	st.mu.Unlock()
	if span == nil {
		return
	}

	// Resolve provider name: prefer explicit Provider, fall back to API key.
	providerName := hctx.Provider
	if providerName == "" {
		providerName = hctx.API
	}
	span.SetAttributes(
		attribute.String("gen_ai.system", hctx.API),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.response.model", hctx.Model),
		attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(hctx.StopReason)}),
		attribute.Int("gen_ai.usage.input_tokens", hctx.Usage.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", hctx.Usage.OutputTokens),
		attribute.Int("gen_ai.usage.total_tokens", hctx.Usage.TotalTokens),
	)
	if hctx.BaseURL != "" {
		span.SetAttributes(attribute.String("server.address", hctx.BaseURL))
	}
	if hctx.Usage.CacheRead > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_read.input_tokens", hctx.Usage.CacheRead))
	}
	if hctx.Usage.CacheWrite > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_creation.input_tokens", hctx.Usage.CacheWrite))
	}
	span.SetAttributes(
		attribute.String("gen_ai.server.duration", hctx.Duration.Round(time.Millisecond).String()),
		attribute.Float64("gen_ai.server.duration_s", hctx.Duration.Seconds()),
	)
	if hctx.TimeToFirstToken > 0 {
		span.SetAttributes(
			attribute.String("gen_ai.server.time_to_first_token", hctx.TimeToFirstToken.Round(time.Millisecond).String()),
			attribute.Float64("gen_ai.server.time_to_first_token_s", hctx.TimeToFirstToken.Seconds()),
		)
	}
	if hctx.Usage.Cost.Total > 0 {
		span.SetAttributes(attribute.Float64("gen_ai.usage.cost_usd", hctx.Usage.Cost.Total))
	}
	if hctx.Attempts > 0 {
		// Retries happen inside the provider SDK, below every span this hook
		// owns; the count is the only place they surface on the parent.
		span.SetAttributes(
			attribute.Int("gen_ai.request.attempts", hctx.Attempts),
			attribute.Int("gen_ai.request.retry_count", hctx.Attempts-1),
		)
	}
	if hctx.Error != nil {
		recordSpanError(span, hctx.Error)
	}
	span.End()
	st.activeOps.Add(-1)
}
