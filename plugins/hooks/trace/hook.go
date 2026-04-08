package trace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vaayne/anna/pkg/hooks"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func init() {
	pkgplugins.Register("hook/trace", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
			ID:           "hook/trace",
			Kind:         "hook",
			Name:         "trace",
			DisplayName:  "Trace",
			Description:  "Capture tracing for LLM, tool, and memory activity.",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityHook,
			},
		})
		host.Registry().RegisterHook(pkgplugins.HookRegistration{
			PluginID: "hook/trace",
			Name:     "trace",
			Build: func(ctx pkgplugins.HookContext) (hooks.HookPlugin, error) {
				return newHook()
			},
		})
	}))
}

// Hook logs LLM, tool, and memory call details via slog, and optionally
// exports OpenTelemetry traces when OTEL_EXPORTER_OTLP_ENDPOINT is set.
type Hook struct {
	log    *slog.Logger
	tracer trace.Tracer
	tp     *sdktrace.TracerProvider // nil when OTel is disabled

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

func newHook() (*Hook, error) {
	cfg := loadConfig()
	log := slog.With("hook", "trace")

	h := &Hook{
		log:      log,
		tracer:   noop.NewTracerProvider().Tracer("anna"),
		sessions: make(map[string]*sessionTrace),
		done:     make(chan struct{}),
	}

	// Only initialize OTel exporter when an endpoint is configured.
	if cfg.Enabled {
		tp, err := initTraceProvider(cfg)
		if err != nil {
			return nil, err
		}
		h.tp = tp
		h.tracer = tp.Tracer("anna")
		// Set as global so otelhttp and other instrumentation libraries use it.
		otel.SetTracerProvider(tp)
		go h.reaper()
		log.Info("otel tracing enabled",
			"endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			"service", cfg.ServiceName)
	}

	return h, nil
}

func initTraceProvider(cfg config) (*sdktrace.TracerProvider, error) {
	ctx := context.Background()
	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: create exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel: create resource: %w", err)
	}

	var sampler sdktrace.Sampler
	switch {
	case cfg.SampleRate >= 1.0:
		sampler = sdktrace.AlwaysSample()
	case cfg.SampleRate <= 0.0:
		sampler = sdktrace.NeverSample()
	default:
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRate)
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	), nil
}

func (*Hook) Name() string  { return "trace" }
func (*Hook) Priority() int { return 0 } // runs first

func (h *Hook) otelEnabled() bool { return h.tp != nil }

// Close flushes pending spans and shuts down the trace provider.
func (h *Hook) Close() error {
	if !h.otelEnabled() {
		return nil
	}
	close(h.done)
	h.mu.Lock()
	for id, st := range h.sessions {
		h.endSession(st)
		delete(h.sessions, id)
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.tp.Shutdown(ctx)
}

// getOrCreateSession returns the session trace, creating root span if needed.
// Caller must hold h.mu.
func (h *Hook) getOrCreateSession(agentID, sessionID string) *sessionTrace {
	key := sessionKey(agentID, sessionID)
	st, ok := h.sessions[key]
	if ok {
		return st
	}
	ctx, chatSpan := h.tracer.Start(context.Background(), "chat",
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
