// Package tracehook is the core agent trace hook: it logs LLM, tool, and
// memory activity via slog and, when OTel tracing is enabled, records the
// session/turn/LLM/tool/memory span hierarchy. It is always part of the agent
// runtime hook set — it is not a user-managed plugin. Span export is owned by
// the global tracer provider set up in package observability; this hook only
// produces spans, resolving the global tracer lazily at span-start time.
package tracehook

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/observability"
)

// Hook logs LLM, tool, and memory call details via slog, and records OTel
// spans when tracing is enabled.
type Hook struct {
	log     *slog.Logger
	enabled bool // mirrors whether OTel export is configured

	mu       sync.Mutex
	sessions map[string]*sessionTrace
	done     chan struct{}
}

// sessionKey builds a composite key to avoid collisions across agents.
func sessionKey(agentID, sessionID string) string {
	return agentID + ":" + sessionID
}

// sessionTrace tracks the active span hierarchy for one chat session.
type sessionTrace struct {
	mu sync.Mutex // protects all fields below

	chatSpan trace.Span
	chatCtx  context.Context

	turnSpan trace.Span
	turnCtx  context.Context
	turnNum  int

	// LLM call span (one at a time per session).
	llmSpan trace.Span

	// Tool call spans keyed by ToolCallID.
	toolSpans map[string]trace.Span

	activeOps  atomic.Int32
	lastActive time.Time
}

// New builds the core trace hook. Whether OTel spans are recorded is decided
// once from the global OTel config; span export is handled by the global
// provider installed during server startup.
func New() *Hook {
	h := &Hook{
		log:      slog.With("hook", "trace"),
		enabled:  observability.LoadConfig().Enabled,
		sessions: make(map[string]*sessionTrace),
		done:     make(chan struct{}),
	}
	if h.enabled {
		go h.reaper()
	}
	return h
}

func (*Hook) Name() string  { return "trace" }
func (*Hook) Priority() int { return 0 } // runs first

func (h *Hook) otelEnabled() bool { return h.enabled }

// tracer resolves the global tracer lazily so spans always use whatever
// provider the observability package installed at startup, never a stale one
// captured at hook construction.
func (*Hook) tracer() trace.Tracer { return otel.Tracer("stella") }

// Close ends any in-flight session spans and stops the reaper. It does not
// shut down the tracer provider — that lifecycle is owned by package
// observability.
func (h *Hook) Close() error {
	if !h.enabled {
		return nil
	}
	close(h.done)
	h.mu.Lock()
	for id, st := range h.sessions {
		h.endSession(st)
		delete(h.sessions, id)
	}
	h.mu.Unlock()
	return nil
}

// getOrCreateSession returns the session trace, creating root span if needed.
// Caller must hold h.mu.
func (h *Hook) getOrCreateSession(agentID, sessionID string) *sessionTrace {
	key := sessionKey(agentID, sessionID)
	st, ok := h.sessions[key]
	if ok {
		return st
	}
	ctx, chatSpan := h.tracer().Start(context.Background(), "chat",
		trace.WithAttributes(
			attribute.String("gen_ai.conversation.id", sessionID),
			attribute.String("agent_id", agentID),
		),
	)
	st = &sessionTrace{
		chatSpan:   chatSpan,
		chatCtx:    ctx,
		toolSpans:  make(map[string]trace.Span),
		lastActive: time.Now(),
	}
	h.sessions[key] = st
	return st
}

// endSession ends all active spans for a session.
func (h *Hook) endSession(st *sessionTrace) {
	if st.llmSpan != nil {
		st.llmSpan.End()
	}
	for _, span := range st.toolSpans {
		span.End()
	}
	if st.turnSpan != nil {
		st.turnSpan.End()
	}
	st.chatSpan.End()
}

// reaper periodically cleans up idle sessions.
func (h *Hook) reaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			h.mu.Lock()
			now := time.Now()
			var expired []string
			for id, st := range h.sessions {
				st.mu.Lock()
				idle := st.activeOps.Load() == 0 && now.Sub(st.lastActive) > 2*time.Minute
				st.mu.Unlock()
				if idle {
					expired = append(expired, id)
				}
			}
			for _, id := range expired {
				st := h.sessions[id]
				h.endSession(st)
				delete(h.sessions, id)
			}
			h.mu.Unlock()
		}
	}
}
