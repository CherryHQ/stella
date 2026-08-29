package tracehook

import (
	"context"
	"fmt"
	"net/url"
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

	llmCtx := ctx
	if h.otelEnabled() && hctx.SessionID != "" {
		h.mu.Lock()
		st := h.getOrCreateSession(ctx, hctx.AgentID, hctx.SessionID, hctx.Channel, hctx.BindingID)
		h.mu.Unlock()

		st.mu.Lock()
		st.turnNum++
		callID := hctx.CallID
		if callID == "" {
			callID = fmt.Sprintf("turn-%d", st.turnNum)
		}
		turnCtx, turnSpan := h.tracer().Start(st.loopCtx, "agent.turn",
			trace.WithAttributes(
				attribute.Int("stella.turn.number", st.turnNum),
				attribute.String("stella.user_id", hctx.UserID),
				attribute.String("stella.agent_id", hctx.AgentID),
				attribute.String("stella.chat.channel", hctx.Channel),
				attribute.String("stella.chat.binding_id", hctx.BindingID),
			),
		)

		attrs := []attribute.KeyValue{
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", hctx.API),
			attribute.String("gen_ai.request.model", hctx.Model),
			attribute.String("gen_ai.conversation.id", hctx.SessionID),
			attribute.String("stella.user_id", hctx.UserID),
			attribute.String("stella.agent_id", hctx.AgentID),
			attribute.String("stella.chat.channel", hctx.Channel),
			attribute.String("stella.chat.binding_id", hctx.BindingID),
			attribute.Int("stella.llm.message_count", hctx.MessageCount),
			attribute.Int("stella.llm.system_prompt_length", len(hctx.System)),
		}
		if hctx.Provider != "" {
			attrs = append(attrs, attribute.String("gen_ai.provider.name", hctx.Provider))
		}
		if host := serverHost(hctx.BaseURL); host != "" {
			attrs = append(attrs, attribute.String("server.address", host))
		}
		if hctx.MaxTokens != nil {
			attrs = append(attrs, attribute.Int("gen_ai.request.max_tokens", *hctx.MaxTokens))
		}
		if hctx.Temperature != nil {
			attrs = append(attrs, attribute.Float64("gen_ai.request.temperature", *hctx.Temperature))
		}
		var llmSpan trace.Span
		llmCtx, llmSpan = h.tracer().Start(turnCtx, fmt.Sprintf("chat %s", hctx.Model), trace.WithAttributes(attrs...))
		st.turnCtx = turnCtx
		st.turnSpans[callID] = turnSpan
		st.llmSpans[callID] = llmSpan
		st.activeOps.Add(1)
		st.lastActive = time.Now()
		st.mu.Unlock()
	}

	h.log.InfoContext(llmCtx, "pre_llm_call",
		"model", hctx.Model,
		"messages", hctx.MessageCount,
		"effective_tool_count", len(tools),
		"effective_tools", tools,
		"system_len", len(hctx.System),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"channel", hctx.Channel,
	)
	if h.otelEnabled() && hctx.SessionID != "" {
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
		"channel", hctx.Channel,
	}
	if hctx.Usage.Cost.Total > 0 {
		attrs = append(attrs, "cost_usd", hctx.Usage.Cost.Total)
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error.type", logErrorClass(hctx.Error), "error.class", "llm_call_failed")
		logRawError("llm call failed", "llm_call_failed", hctx.Error)
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

	st.mu.Lock()
	callID := hctx.CallID
	if callID == "" && len(st.llmSpans) == 1 {
		for id := range st.llmSpans {
			callID = id
		}
	}
	span := st.llmSpans[callID]
	delete(st.llmSpans, callID)
	if span != nil {
		st.lastActive = time.Now()
	}
	st.mu.Unlock()
	if span == nil {
		return
	}

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
		attribute.Int("stella.llm.total_tokens", hctx.Usage.TotalTokens),
		attribute.StringSlice("stella.llm.provider_tool_names", hctx.ProviderToolNames),
		attribute.Int("stella.llm.provider_tool_count", len(hctx.ProviderToolNames)),
		attribute.Int("stella.code.catalog_size", hctx.CodeCatalogSize),
		attribute.String("stella.chat.channel", hctx.Channel),
		attribute.String("stella.chat.binding_id", hctx.BindingID),
	)
	if host := serverHost(hctx.BaseURL); host != "" {
		span.SetAttributes(attribute.String("server.address", host))
	}
	if hctx.Usage.CacheRead > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_read.input_tokens", hctx.Usage.CacheRead))
	}
	if hctx.Usage.CacheWrite > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_creation.input_tokens", hctx.Usage.CacheWrite))
	}
	span.SetAttributes(
		attribute.Float64("stella.llm.call.duration_s", hctx.Duration.Seconds()),
	)
	if hctx.TimeToFirstToken > 0 {
		span.SetAttributes(attribute.Float64("stella.llm.time_to_first_token_s", hctx.TimeToFirstToken.Seconds()))
	}
	if hctx.Usage.Cost.Total > 0 {
		span.SetAttributes(attribute.Float64("stella.llm.cost_usd", hctx.Usage.Cost.Total))
	}
	if hctx.Attempts > 0 {
		span.SetAttributes(
			attribute.Int("stella.llm.attempts", hctx.Attempts),
			attribute.Int("stella.llm.retry_count", hctx.Attempts-1),
		)
	}
	if hctx.Error != nil {
		recordSpanError(span, hctx.Error, "model call failed")
	}
	span.End()
	st.mu.Lock()
	st.activeOps.Add(-1)
	st.lastActive = time.Now()
	st.mu.Unlock()
}

// serverHost reduces a configured base URL to its host. The rest of the URL is
// not identifying information a trace needs, and a gateway base URL can carry
// an API key in its path or query string.
func serverHost(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}
