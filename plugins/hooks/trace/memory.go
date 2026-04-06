package trace

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/pkg/hooks"
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
