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
		"user_id", hctx.UserID,
	)

	if h.otelEnabled() {
		h.mu.Lock()
		st := h.getOrCreateSession(hctx.SessionID)

		if st.turnSpan != nil {
			st.turnSpan.End()
		}
		st.turnNum++
		st.turnCtx, st.turnSpan = h.tracer.Start(st.chatCtx,
			fmt.Sprintf("turn %d", st.turnNum),
			trace.WithAttributes(
				attribute.Int("anna.turn.number", st.turnNum),
				attribute.Int64("user_id", hctx.UserID),
				attribute.String("agent_id", hctx.AgentID),
			),
		)

		_, st.llmSpan = h.tracer.Start(st.turnCtx, "gen_ai.chat",
			trace.WithAttributes(
				attribute.String("gen_ai.operation.name", "chat"),
				attribute.String("gen_ai.request.model", hctx.Model),
				attribute.String("gen_ai.conversation.id", hctx.SessionID),
				attribute.Int64("user_id", hctx.UserID),
				attribute.String("agent_id", hctx.AgentID),
				attribute.Int("gen_ai.request.message_count", hctx.MessageCount),
				attribute.Int("gen_ai.request.tool_count", len(tools)),
				attribute.StringSlice("gen_ai.request.tool_names", tools),
				attribute.Int("gen_ai.request.system_prompt_len", len(hctx.System)),
			),
		)
		st.activeOps.Add(1)
		st.lastActive = time.Now()
		h.mu.Unlock()
	}

	return hooks.PreLLMCallResult{}, nil
}

func (h *Hook) OnPostLLMCall(_ context.Context, hctx *hooks.PostLLMCallContext) {
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
		"user_id", hctx.UserID,
	}
	if hctx.Usage.Cost.Total > 0 {
		attrs = append(attrs, "cost_usd", hctx.Usage.Cost.Total)
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.Info("post_llm_call", attrs...)

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
		attribute.Int("gen_ai.usage.total_tokens", hctx.Usage.TotalTokens),
	)
	if hctx.Usage.CacheRead > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_read.input_tokens", hctx.Usage.CacheRead))
	}
	if hctx.Usage.CacheWrite > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_creation.input_tokens", hctx.Usage.CacheWrite))
	}
	if hctx.TimeToFirstToken > 0 {
		span.SetAttributes(attribute.Float64("gen_ai.server.time_to_first_token_s", hctx.TimeToFirstToken.Seconds()))
	}
	if hctx.Usage.Cost.Total > 0 {
		span.SetAttributes(attribute.Float64("gen_ai.usage.cost_usd", hctx.Usage.Cost.Total))
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
