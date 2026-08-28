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

	"github.com/CherryHQ/stella/internal/agent/toolmeta"
)

// Hook logs LLM, tool, and memory call details via slog, and records OTel
// spans when tracing is enabled.
type Hook struct {
	log      *slog.Logger
	enabled  bool // mirrors whether OTel export is configured
	recordIO bool // record full tool input/result text on spans (opt-in)
	toolMeta *toolmeta.Registry

	mu        sync.Mutex
	sessions  map[string]*sessionTrace
	done      chan struct{}
	startOnce sync.Once
}

// sessionKey builds a composite key to avoid collisions across agents.
func sessionKey(agentID, sessionID string) string {
	return agentID + ":" + sessionID
}

// sessionTrace tracks the active span hierarchy for one chat session.
type sessionTrace struct {
	mu sync.Mutex // protects all fields below

	loopSpan trace.Span
	loopCtx  context.Context

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

// New builds the core trace hook. enabled mirrors whether the global tracer
// provider was installed (see observability.Init); recordIO is the
// OTEL_STELLA_RECORD_TOOL_IO opt-in. Both are passed by the caller so the hook
// and the global provider share a single source of truth instead of each
// reading the environment. Span export is handled by that global provider.
func New(enabled, recordIO bool, opts ...Option) *Hook {
	// The constructor starts no goroutine (issue #708 Section D). The idle-session
	// reaper is launched by the composition root via Start(ctx).
	h := &Hook{
		log:      slog.With("hook", "trace"),
		enabled:  enabled,
		recordIO: recordIO,
		sessions: make(map[string]*sessionTrace),
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Option configures optional Hook dependencies.
type Option func(*Hook)

// WithToolMeta supplies the generated-tool declarations, so the `action` span
// attribute can come from the tool's name. Without it a split tool reports no
// action: the argument the union carried is gone.
func WithToolMeta(reg *toolmeta.Registry) Option {
	return func(h *Hook) { h.toolMeta = reg }
}

// Start launches the idle-session reaper. It is a no-op when tracing is disabled
// and is idempotent. The composition root calls it once after New with the
// daemon lifecycle context, so the reaper exits on ctx cancellation; Close also
// stops it during the reverse-Close shutdown phase.
func (h *Hook) Start(ctx context.Context) {
	if !h.enabled {
		return
	}
	h.startOnce.Do(func() { go h.reaper(ctx) })
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

// getOrCreateSession returns the session trace, creating the agent loop span if
// needed. The loop span is parented on parentCtx — the inbound request context
// (HTTP/channel entry) — so the whole agent trace nests under that span instead
// of starting a disconnected root. Cancellation is stripped because the loop
// outlives the HTTP request: a cancelled parent must not tear down the
// session's long-lived spans, and span linkage only needs the parent's span
// context, not its deadline. Caller must hold h.mu.
func (h *Hook) getOrCreateSession(parentCtx context.Context, agentID, sessionID string) *sessionTrace {
	key := sessionKey(agentID, sessionID)
	st, ok := h.sessions[key]
	if ok {
		return st
	}
	ctx, loopSpan := h.tracer().Start(context.WithoutCancel(parentCtx), "agent.loop",
		trace.WithAttributes(
			attribute.String("gen_ai.conversation.id", sessionID),
			attribute.String("agent_id", agentID),
		),
	)
	st = &sessionTrace{
		loopSpan:   loopSpan,
		loopCtx:    ctx,
		toolSpans:  make(map[string]trace.Span),
		lastActive: time.Now(),
	}
	h.sessions[key] = st
	return st
}

// endSession ends all active spans for a session. It snapshots the span
// fields under st.mu and clears them so a concurrent reaper/Close and an
// in-flight callback can never double-End the same span.
func (h *Hook) endSession(st *sessionTrace) {
	st.mu.Lock()
	llmSpan := st.llmSpan
	toolSpans := st.toolSpans
	turnSpan := st.turnSpan
	loopSpan := st.loopSpan
	st.llmSpan = nil
	st.toolSpans = make(map[string]trace.Span)
	st.turnSpan = nil
	st.loopSpan = nil
	st.mu.Unlock()

	if llmSpan != nil {
		llmSpan.End()
	}
	for _, span := range toolSpans {
		span.End()
	}
	if turnSpan != nil {
		turnSpan.End()
	}
	if loopSpan != nil {
		loopSpan.End()
	}
}

// reaper periodically cleans up idle sessions. It exits on ctx cancellation
// (composition-root shutdown) or Close (done channel).
func (h *Hook) reaper(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
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
