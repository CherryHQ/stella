package trace

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/vaayne/anna/pkg/hooks"
)

func (h *Hook) OnPreAgentCall(_ context.Context, hctx *hooks.PreAgentCallContext) {
	h.log.Info("pre_agent_call",
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"message_len", hctx.MessageLen,
	)

	if h.otelEnabled() {
		h.mu.Lock()
		h.getOrCreateSession(hctx.SessionID)
		h.mu.Unlock()
	}
}

func (h *Hook) OnPostAgentCall(_ context.Context, hctx *hooks.PostAgentCallContext) {
	attrs := []any{
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"duration", hctx.Duration.Round(time.Millisecond),
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.Info("post_agent_call", attrs...)

	if !h.otelEnabled() {
		return
	}

	h.mu.Lock()
	st := h.sessions[hctx.SessionID]
	if st == nil {
		h.mu.Unlock()
		return
	}
	// End turn span if still open.
	if st.turnSpan != nil {
		st.turnSpan.End()
		st.turnSpan = nil
	}
	// End the root chat span — the chat request is complete.
	st.chatSpan.SetAttributes(
		attribute.Float64("anna.chat.duration_s", hctx.Duration.Seconds()),
	)
	if hctx.Error != nil {
		st.chatSpan.RecordError(hctx.Error)
	}
	st.chatSpan.End()
	delete(h.sessions, hctx.SessionID)
	h.mu.Unlock()
}
