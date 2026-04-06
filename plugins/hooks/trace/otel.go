package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/vaayne/anna/pkg/hooks"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
)

func init() {
	pluginhooks.Register("trace", pluginhooks.Registration{
		Factory: func(_ pluginhooks.BuildContext) (hooks.HookPlugin, error) {
			return newHook()
		},
	})
}

// levelTrace is below slog.LevelDebug. When enabled, memory traces include
// the full Detail field (message content, search results, profile text).
const levelTrace = slog.Level(-8)

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

// sessionTrace tracks the active span hierarchy for one chat session.
type sessionTrace struct {
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
	if cfg.Endpoint != "" {
		tp, err := initTraceProvider(cfg)
		if err != nil {
			return nil, err
		}
		h.tp = tp
		h.tracer = tp.Tracer("anna")
		go h.reaper()
		log.Info("otel tracing enabled", "endpoint", cfg.Endpoint, "service", cfg.ServiceName)
	}

	return h, nil
}

func initTraceProvider(cfg config) (*sdktrace.TracerProvider, error) {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()))
	}

	ctx := context.Background()
	exporter, err := otlptracegrpc.New(ctx, opts...)
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

// --- PreLLMCall ---

func (h *Hook) OnPreLLMCall(_ context.Context, hctx *hooks.PreLLMCallContext) (hooks.PreLLMCallResult, error) {
	// slog tracing
	tools := make([]string, len(hctx.ToolDefinitions))
	for i, t := range hctx.ToolDefinitions {
		tools[i] = t.Name
	}
	h.log.Info("pre_llm_call",
		"model", hctx.Model,
		"messages", hctx.MessageCount,
		"tools", len(tools),
		"tool_names", tools,
		"system_len", len(hctx.System),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	)

	// OTel spans
	if h.otelEnabled() {
		h.mu.Lock()
		st := h.getOrCreateSession(hctx.SessionID)

		// Rotate turn: end previous turn span.
		if st.turnSpan != nil {
			st.turnSpan.End()
		}
		st.turnNum++
		st.turnCtx, st.turnSpan = h.tracer.Start(st.chatCtx,
			fmt.Sprintf("turn %d", st.turnNum),
		)

		// Start LLM call span as child of turn.
		_, st.llmSpan = h.tracer.Start(st.turnCtx, "gen_ai.chat",
			trace.WithAttributes(
				attribute.String("gen_ai.operation.name", "chat"),
				attribute.String("gen_ai.request.model", hctx.Model),
				attribute.String("gen_ai.conversation.id", hctx.SessionID),
			),
		)
		st.activeOps.Add(1)
		st.lastActive = time.Now()
		h.mu.Unlock()
	}

	return hooks.PreLLMCallResult{}, nil
}

// --- PostLLMCall ---

func (h *Hook) OnPostLLMCall(_ context.Context, hctx *hooks.PostLLMCallContext) {
	// slog tracing
	attrs := []any{
		"provider", hctx.Provider,
		"model", hctx.Model,
		"stop_reason", hctx.StopReason,
		"duration", hctx.Duration.Round(time.Millisecond),
		"ttft", hctx.TimeToFirstToken.Round(time.Millisecond),
		"input_tokens", hctx.Usage.InputTokens,
		"output_tokens", hctx.Usage.OutputTokens,
		"cache_read", hctx.Usage.CacheRead,
		"cache_write", hctx.Usage.CacheWrite,
		"total_tokens", hctx.Usage.TotalTokens,
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	}
	if hctx.Error != nil {
		attrs = append(attrs, "error", hctx.Error)
	}
	h.log.Info("post_llm_call", attrs...)

	// OTel spans
	if !h.otelEnabled() {
		return
	}
	h.mu.Lock()
	st := h.sessions[hctx.SessionID]
	h.mu.Unlock()
	if st == nil || st.llmSpan == nil {
		return
	}

	span := st.llmSpan
	span.SetAttributes(
		attribute.String("gen_ai.provider.name", hctx.Provider),
		attribute.String("gen_ai.response.model", hctx.Model),
		attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(hctx.StopReason)}),
		attribute.Int("gen_ai.usage.input_tokens", hctx.Usage.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", hctx.Usage.OutputTokens),
	)
	if hctx.Usage.CacheRead > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_read.input_tokens", hctx.Usage.CacheRead))
	}
	if hctx.Usage.CacheWrite > 0 {
		span.SetAttributes(attribute.Int("gen_ai.usage.cache_creation.input_tokens", hctx.Usage.CacheWrite))
	}
	if hctx.TimeToFirstToken > 0 {
		span.SetAttributes(attribute.Float64("gen_ai.server.time_to_first_token", hctx.TimeToFirstToken.Seconds()))
	}
	if hctx.Error != nil {
		span.RecordError(hctx.Error)
		span.SetStatus(codes.Error, hctx.Error.Error())
		span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", hctx.Error)))
	}
	span.End()

	st.llmSpan = nil
	st.activeOps.Add(-1)
	st.lastActive = time.Now()
}

// --- PreToolCall ---

func (h *Hook) OnPreToolCall(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	// slog tracing
	input := summarizeArgs(hctx.ToolName, hctx.Arguments)
	h.log.Info("pre_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"input", input,
		"session_id", hctx.SessionID,
	)

	// OTel spans
	if h.otelEnabled() {
		h.mu.Lock()
		st := h.sessions[hctx.SessionID]
		h.mu.Unlock()
		if st != nil {
			parentCtx := st.turnCtx
			if parentCtx == nil {
				parentCtx = st.chatCtx
			}

			_, span := h.tracer.Start(parentCtx, "gen_ai.execute_tool",
				trace.WithAttributes(
					attribute.String("gen_ai.operation.name", "execute_tool"),
					attribute.String("gen_ai.tool.name", hctx.ToolName),
					attribute.String("gen_ai.tool.call.id", hctx.ToolCallID),
				),
			)

			h.mu.Lock()
			st.toolSpans[hctx.ToolCallID] = span
			st.activeOps.Add(1)
			st.lastActive = time.Now()
			h.mu.Unlock()
		}
	}

	return hooks.PreToolCallResult{}, nil
}

// --- PostToolCall ---

func (h *Hook) OnPostToolCall(_ context.Context, hctx *hooks.PostToolCallContext) {
	// slog tracing
	resultSnippet := hctx.Result
	if len(resultSnippet) > 200 {
		resultSnippet = resultSnippet[:200] + "..."
	}
	h.log.Info("post_tool_call",
		"tool", hctx.ToolName,
		"call_id", hctx.ToolCallID,
		"is_error", hctx.IsError,
		"duration", hctx.Duration,
		"result_len", len(hctx.Result),
		"result", resultSnippet,
		"session_id", hctx.SessionID,
	)

	// OTel spans
	if !h.otelEnabled() {
		return
	}
	h.mu.Lock()
	st := h.sessions[hctx.SessionID]
	if st == nil {
		h.mu.Unlock()
		return
	}
	span, ok := st.toolSpans[hctx.ToolCallID]
	if ok {
		delete(st.toolSpans, hctx.ToolCallID)
	}
	h.mu.Unlock()

	if !ok || span == nil {
		return
	}

	if hctx.IsError {
		span.SetStatus(codes.Error, "tool execution failed")
		span.SetAttributes(attribute.String("error.type", "tool_error"))
	}
	span.End()

	st.activeOps.Add(-1)
	st.lastActive = time.Now()
}

// --- PostMemoryCall ---

func (h *Hook) OnPostMemoryCall(_ context.Context, hctx *hooks.PostMemoryCallContext) {
	// slog tracing
	logAttrs := []any{
		"op", string(hctx.Op),
		"duration", hctx.Duration.Round(time.Millisecond),
		"session_id", hctx.SessionID,
		"agent_id", hctx.AgentID,
	}
	if hctx.MessageCount > 0 {
		logAttrs = append(logAttrs, "message_count", hctx.MessageCount)
	}
	if hctx.TokenCount > 0 {
		logAttrs = append(logAttrs, "token_count", hctx.TokenCount)
	}
	if hctx.TokenDelta != 0 {
		logAttrs = append(logAttrs, "token_delta", hctx.TokenDelta)
	}
	if hctx.SummaryCount > 0 {
		logAttrs = append(logAttrs, "summary_count", hctx.SummaryCount)
	}
	if hctx.ResultCount > 0 {
		logAttrs = append(logAttrs, "result_count", hctx.ResultCount)
	}
	if hctx.Error != nil {
		logAttrs = append(logAttrs, "error", hctx.Error)
	}
	if hctx.Detail != "" && h.log.Enabled(context.Background(), levelTrace) {
		logAttrs = append(logAttrs, "detail", hctx.Detail)
	}
	h.log.Info("post_memory_call", logAttrs...)

	// OTel spans — memory has post-hook only, so backdate the span.
	if !h.otelEnabled() {
		return
	}

	h.mu.Lock()
	st := h.sessions[hctx.SessionID]
	h.mu.Unlock()

	parentCtx := context.Background()
	if st != nil {
		if st.turnCtx != nil {
			parentCtx = st.turnCtx
		} else {
			parentCtx = st.chatCtx
		}
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

	if st != nil {
		st.lastActive = time.Now()
	}
}

// --- Internal helpers ---

// getOrCreateSession returns the session trace, creating root span if needed.
// Caller must hold h.mu.
func (h *Hook) getOrCreateSession(sessionID string) *sessionTrace {
	st, ok := h.sessions[sessionID]
	if ok {
		return st
	}
	ctx, chatSpan := h.tracer.Start(context.Background(), "chat",
		trace.WithAttributes(
			attribute.String("gen_ai.conversation.id", sessionID),
		),
	)
	st = &sessionTrace{
		chatSpan:   chatSpan,
		chatCtx:    ctx,
		toolSpans:  make(map[string]trace.Span),
		lastActive: time.Now(),
	}
	h.sessions[sessionID] = st
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
			for id, st := range h.sessions {
				if st.activeOps.Load() == 0 && now.Sub(st.lastActive) > 2*time.Minute {
					h.endSession(st)
					delete(h.sessions, id)
				}
			}
			h.mu.Unlock()
		}
	}
}

func summarizeArgs(tool string, args map[string]any) string {
	switch tool {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 200 {
				return cmd[:200] + "..."
			}
			return cmd
		}
	case "read", "write", "edit":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	s := string(data)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
