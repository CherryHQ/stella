package tracehook

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/hooks"
)

// levelTrace is below slog.LevelDebug. When enabled, memory traces include
// the full Detail field (message content, search results, profile text).
const levelTrace = slog.Level(-8)

type memorySpanKey struct{}

func memoryResultAttrs(hctx *hooks.PostMemoryCallContext) []attribute.KeyValue {
	spanAttrs := []attribute.KeyValue{
		attribute.Float64("stella.memory.duration_s", hctx.Duration.Seconds()),
	}
	if hctx.MessageCount > 0 {
		spanAttrs = append(spanAttrs, attribute.Int("stella.memory.message_count", hctx.MessageCount))
	}
	if hctx.TokenCount > 0 {
		spanAttrs = append(spanAttrs, attribute.Int("stella.memory.token_count", hctx.TokenCount))
	}
	if hctx.TokenDelta != 0 {
		spanAttrs = append(spanAttrs, attribute.Int("stella.memory.token_delta", hctx.TokenDelta))
	}
	if hctx.SummaryCount > 0 {
		spanAttrs = append(spanAttrs, attribute.Int("stella.memory.summary_count", hctx.SummaryCount))
	}
	if hctx.ResultCount > 0 {
		spanAttrs = append(spanAttrs, attribute.Int("stella.memory.result_count", hctx.ResultCount))
	}
	return spanAttrs
}

func (h *Hook) OnPreMemoryCall(ctx context.Context, hctx *hooks.PreMemoryCallContext) (hooks.PreMemoryCallResult, error) {
	if !h.otelEnabled() {
		return hooks.PreMemoryCallResult{}, nil
	}

	var st *sessionTrace
	parentCtx := ctx
	if hctx.SessionID != "" {
		key := sessionKey(hctx.AgentID, hctx.SessionID)
		h.mu.Lock()
		st = h.sessions[key]
		h.mu.Unlock()
		if st != nil {
			st.mu.Lock()
			parentCtx = st.loopCtx
			if st.turnCtx != nil {
				parentCtx = st.turnCtx
			}
			st.mu.Unlock()
		}
	}
	if st == nil && !trace.SpanContextFromContext(parentCtx).IsValid() {
		return hooks.PreMemoryCallResult{}, nil
	}

	spanAttrs := []attribute.KeyValue{
		attribute.String("stella.memory.op", string(hctx.Op)),
		attribute.String("stella.chat.channel", hctx.Channel),
		attribute.String("stella.chat.binding_id", hctx.BindingID),
		attribute.String("stella.agent_id", hctx.AgentID),
	}
	if hctx.SessionID != "" {
		spanAttrs = append(spanAttrs, attribute.String("stella.memory.session_id", hctx.SessionID))
	}
	spanCtx, span := h.tracer().Start(parentCtx, fmt.Sprintf("memory.%s", hctx.Op),
		trace.WithAttributes(spanAttrs...),
	)
	if st != nil {
		st.activeOps.Add(1)
		st.mu.Lock()
		st.lastActive = time.Now()
		st.mu.Unlock()
	}

	return hooks.PreMemoryCallResult{Context: context.WithValue(spanCtx, memorySpanKey{}, span)}, nil
}

func (h *Hook) OnPostMemoryCall(ctx context.Context, hctx *hooks.PostMemoryCallContext) {
	attrs := []any{
		"op", string(hctx.Op),
		"duration", hctx.Duration.Round(time.Millisecond),
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"channel", hctx.Channel,
	}
	if hctx.SessionID != "" {
		attrs = append(attrs, "session_id", hctx.SessionID)
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
	if hctx.SessionID == "" && hctx.Error == nil && hctx.Op != hooks.MemoryOpCompact {
		h.log.DebugContext(ctx, "post_memory_call", attrs...)
	} else {
		h.log.InfoContext(ctx, "post_memory_call", attrs...)
	}

	if !h.otelEnabled() {
		return
	}

	span, ok := ctx.Value(memorySpanKey{}).(trace.Span)
	if !ok || span == nil {
		return
	}
	span.SetAttributes(memoryResultAttrs(hctx)...)
	if hctx.Error != nil {
		recordSpanError(span, hctx.Error, "memory operation failed")
	}
	span.End()

	key := sessionKey(hctx.AgentID, hctx.SessionID)
	h.mu.Lock()
	st := h.sessions[key]
	h.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	st.activeOps.Add(-1)
	st.lastActive = time.Now()
	st.mu.Unlock()
}
