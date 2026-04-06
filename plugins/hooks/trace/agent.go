package trace

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

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
		st := h.getOrCreateSession(hctx.AgentID, hctx.SessionID)
		st.mu.Lock()
		st.chatSpan.SetAttributes(
			attribute.Int64("user_id", hctx.UserID),
			attribute.String("agent_id", hctx.AgentID),
			attribute.Int("anna.chat.message_len", hctx.MessageLen),
		)
		st.mu.Unlock()
		h.mu.Unlock()
	}
}

func (h *Hook) OnPostAgentCall(_ context.Context, hctx *hooks.PostAgentCallContext) {
	attrs := []any{
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"duration", hctx.Duration.Round(time.Millisecond),
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.Info("post_agent_call", attrs...)

	if !h.otelEnabled() {
		return
	}

	key := sessionKey(hctx.AgentID, hctx.SessionID)
	h.mu.Lock()
	st := h.sessions[key]
	if st == nil {
		h.mu.Unlock()
		return
	}
	delete(h.sessions, key)
	h.mu.Unlock()

	st.mu.Lock()
	// Set final attributes before closing the session.
	st.chatSpan.SetAttributes(
		attribute.Float64("anna.chat.duration_s", hctx.Duration.Seconds()),
		attribute.Int("anna.chat.turn_count", st.turnNum),
	)
	if hctx.Error != nil {
		st.chatSpan.RecordError(hctx.Error)
		st.chatSpan.SetStatus(codes.Error, hctx.Error.Error())
		st.chatSpan.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", hctx.Error)))
	}
	st.mu.Unlock()

	// End all remaining spans (tool, LLM, turn, chat).
	h.endSession(st)
}
