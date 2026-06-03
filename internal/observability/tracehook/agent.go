package tracehook

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/CherryHQ/stella/pkg/hooks"
)

func (h *Hook) OnPreAgentCall(ctx context.Context, hctx *hooks.PreAgentCallContext) {
	h.log.Info("pre_agent_call",
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"message_len", hctx.MessageLen,
		"channel", hctx.Channel,
	)

	if h.otelEnabled() && hctx.SessionID != "" {
		h.mu.Lock()
		st := h.getOrCreateSession(ctx, hctx.AgentID, hctx.SessionID)
		st.mu.Lock()
		st.loopSpan.SetAttributes(
			attribute.String("user_id", hctx.UserID),
			attribute.String("agent_id", hctx.AgentID),
			attribute.Int("stella.agent_loop.message_len", hctx.MessageLen),
		)
		if hctx.Channel != "" {
			st.loopSpan.SetAttributes(attribute.String("stella.agent_loop.channel", hctx.Channel))
		}
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
	st.loopSpan.SetAttributes(
		attribute.Float64("stella.agent_loop.duration_s", hctx.Duration.Seconds()),
		attribute.Int("stella.agent_loop.turn_count", st.turnNum),
	)
	if hctx.Error != nil {
		st.loopSpan.RecordError(hctx.Error)
		st.loopSpan.SetStatus(codes.Error, hctx.Error.Error())
		st.loopSpan.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", hctx.Error)))
	}
	st.mu.Unlock()

	// End all remaining spans (tool, LLM, turn, agent loop).
	h.endSession(st)
}
