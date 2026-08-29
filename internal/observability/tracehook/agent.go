package tracehook

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/CherryHQ/stella/pkg/hooks"
)

func (h *Hook) OnPreAgentCall(ctx context.Context, hctx *hooks.PreAgentCallContext) {
	h.log.InfoContext(ctx, "pre_agent_call",
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"message_len", hctx.MessageLen,
		"channel", hctx.Channel,
	)

	if h.otelEnabled() && hctx.SessionID != "" {
		h.mu.Lock()
		st := h.getOrCreateSession(ctx, hctx.AgentID, hctx.SessionID, hctx.Channel, hctx.BindingID)
		st.mu.Lock()
		st.loopSpan.SetAttributes(
			attribute.String("stella.user_id", hctx.UserID),
			attribute.String("stella.agent_id", hctx.AgentID),
			attribute.Int("stella.agent.message_len", hctx.MessageLen),
			attribute.String("stella.chat.channel", hctx.Channel),
			attribute.String("stella.chat.binding_id", hctx.BindingID),
		)

		st.mu.Unlock()
		h.mu.Unlock()
	}
}

func (h *Hook) OnPostAgentCall(ctx context.Context, hctx *hooks.PostAgentCallContext) {
	attrs := []any{
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
		"user_id", hctx.UserID,
		"duration", hctx.Duration.Round(time.Millisecond),
		"channel", hctx.Channel,
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error.type", logErrorClass(hctx.Error), "error.class", "agent_call_failed")
		logRawError("agent call failed", "agent_call_failed", hctx.Error)
	}
	h.log.InfoContext(ctx, "post_agent_call", attrs...)

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
	st.mu.Lock()
	// A session may have more than one admitted turn. Do not retire the shared
	// loop span until this callback is the last active operation.
	st.loopSpan.SetAttributes(
		attribute.Float64("stella.agent.turn.duration_s", hctx.Duration.Seconds()),
		attribute.Int("stella.agent.turn.count", st.turnNum),
	)
	if hctx.Error != nil {
		recordSpanError(st.loopSpan, hctx.Error, "agent call failed")
	}
	idle := st.activeOps.Load() == 0 && len(st.llmSpans) == 0 && len(st.toolSpans) == 0
	st.mu.Unlock()
	if idle {
		delete(h.sessions, key)
	}
	h.mu.Unlock()
	if idle {
		// End all remaining spans (tool, LLM, turn, agent loop).
		h.endSession(st)
	}
}
