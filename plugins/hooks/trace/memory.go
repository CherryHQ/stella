package trace

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/hooks"
)

// levelTrace is below slog.LevelDebug. When enabled, memory traces include
// the full Detail field (message content, search results, profile text).
const levelTrace = slog.Level(-8)

func (h *Hook) OnPostMemoryCall(_ context.Context, hctx *hooks.PostMemoryCallContext) {
	attrs := []any{
		"op", string(hctx.Op),
		"duration", hctx.Duration.Round(time.Millisecond),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
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
	h.log.Info("post_memory_call", attrs...)

	// Memory has post-hook only — create an instant span.
	// Skip ops with no session ID (e.g. ListInfo is a global query).
	if !h.otelEnabled() || hctx.SessionID == "" {
		return
	}

	key := sessionKey(hctx.AgentID, hctx.SessionID)
	h.mu.Lock()
	st := h.sessions[key]
	h.mu.Unlock()
	if st == nil {
		// No active chat session — this is a pool management call
		// (e.g. session lookup, archival). Skip OTel span.
		return
	}

	st.mu.Lock()
	parentCtx := st.chatCtx
	if st.turnCtx != nil {
		parentCtx = st.turnCtx
	}
	st.mu.Unlock()

	spanAttrs := []attribute.KeyValue{
		attribute.Float64("stella.memory.duration_s", hctx.Duration.Seconds()),
		attribute.String("stella.memory.op", string(hctx.Op)),
		attribute.String("stella.memory.session_id", hctx.SessionID),
		attribute.String("user_id", hctx.UserID),
		attribute.String("agent_id", hctx.AgentID),
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

	_, span := h.tracer.Start(parentCtx, fmt.Sprintf("memory.%s", hctx.Op),
		trace.WithAttributes(spanAttrs...),
	)
	if hctx.Error != nil {
		span.RecordError(hctx.Error)
		span.SetStatus(codes.Error, hctx.Error.Error())
	}
	span.End()

	st.mu.Lock()
	st.lastActive = time.Now()
	st.mu.Unlock()
}
