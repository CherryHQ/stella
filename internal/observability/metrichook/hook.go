// Package metrichook records bounded agent-loop metrics from the core hook
// payloads. It has no session state and never uses session or user identifiers
// as metric labels.
package metrichook

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type QueueStats interface {
	QueueDepth() int
	DroppedCount() int64
}

type Hook struct {
	queue   QueueStats
	active  func() int64
	mu      sync.Mutex
	bound   bool
	metrics metrics
}

type metrics struct {
	llmDuration, llmTTFT, toolDuration, turnDuration, memoryDuration metric.Float64Histogram
	llmTokens, llmCalls, llmAttempts, toolCalls, agentTurns          metric.Int64Counter
	llmCost                                                          metric.Float64Counter
	queueDropped                                                     metric.Int64Counter
	queueDepth, activeSessions                                       metric.Int64ObservableGauge
	callback                                                         metric.Registration
}

func New(queue QueueStats, activeSessions func() int64) *Hook {
	return &Hook{queue: queue, active: activeSessions}
}

func (h *Hook) Name() string  { return "metrics" }
func (h *Hook) Priority() int { return 20 }

// Bind creates instruments against the already-installed global provider. It
// must happen after observability.Init, otherwise instruments bind to noop.
func (h *Hook) Bind(meter metric.Meter) error {
	if h == nil {
		return errors.New("metrics hook is nil")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bound {
		return errors.New("metrics hook already bound")
	}
	if meter == nil {
		return errors.New("metrics meter is nil")
	}
	var err error
	newHistogram := func(name, unit string) metric.Float64Histogram {
		if err != nil {
			return nil
		}
		var out metric.Float64Histogram
		out, err = meter.Float64Histogram(name, metric.WithUnit(unit))
		return out
	}
	newCounter := func(name, unit string) metric.Int64Counter {
		if err != nil {
			return nil
		}
		var out metric.Int64Counter
		out, err = meter.Int64Counter(name, metric.WithUnit(unit))
		return out
	}
	newFloatCounter := func(name, unit string) metric.Float64Counter {
		if err != nil {
			return nil
		}
		var out metric.Float64Counter
		out, err = meter.Float64Counter(name, metric.WithUnit(unit))
		return out
	}
	h.metrics.llmDuration = newHistogram("stella.llm.call.duration", "s")
	h.metrics.llmTTFT = newHistogram("stella.llm.time_to_first_token", "s")
	h.metrics.toolDuration = newHistogram("stella.tool.call.duration", "s")
	h.metrics.turnDuration = newHistogram("stella.agent.turn.duration", "s")
	h.metrics.memoryDuration = newHistogram("stella.memory.op.duration", "s")
	h.metrics.llmTokens = newCounter("stella.llm.tokens", "token")
	h.metrics.llmCalls = newCounter("stella.llm.calls", "{call}")
	h.metrics.llmAttempts = newCounter("stella.llm.attempts", "{attempt}")
	h.metrics.toolCalls = newCounter("stella.tool.calls", "{call}")
	h.metrics.agentTurns = newCounter("stella.agent.turns", "{turn}")
	h.metrics.queueDropped = newCounter("stella.llm_usage.queue.dropped", "{observation}")
	h.metrics.llmCost = newFloatCounter("stella.llm.cost", "USD")
	if err != nil {
		return err
	}
	var gaugeErr error
	h.metrics.queueDepth, gaugeErr = meter.Int64ObservableGauge("stella.llm_usage.queue.depth", metric.WithUnit("{observation}"))
	if gaugeErr != nil {
		return gaugeErr
	}
	h.metrics.activeSessions, gaugeErr = meter.Int64ObservableGauge("stella.trace.sessions.active", metric.WithUnit("{session}"))
	if gaugeErr != nil {
		return gaugeErr
	}
	h.metrics.callback, gaugeErr = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if h.queue != nil {
			o.ObserveInt64(h.metrics.queueDepth, int64(h.queue.QueueDepth()))
		}
		if h.active != nil {
			o.ObserveInt64(h.metrics.activeSessions, h.active())
		}
		return nil
	}, h.metrics.queueDepth, h.metrics.activeSessions)
	if gaugeErr != nil {
		return gaugeErr
	}
	h.bound = true
	return nil
}

func (h *Hook) ready() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bound
}

func (h *Hook) RecordQueueDrop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	bound := h.bound
	counter := h.metrics.queueDropped
	h.mu.Unlock()
	if bound {
		counter.Add(context.Background(), 1)
	}
}

func attrs(values ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(values...)
}

func common(meta hooks.HookMeta) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, 3)
	if meta.AgentID != "" {
		out = append(out, attribute.String("agent_id", meta.AgentID))
	}
	if meta.Channel != "" {
		out = append(out, attribute.String("channel", channel(meta.Channel)))
	}
	return out
}

func channel(value string) string {
	switch value {
	case "web", "telegram", "feishu", "discord", "qq", "wechat", "scheduler", "goal":
		return value
	default:
		return "other"
	}
}

func errorType(err error) string {
	if err == nil {
		return "none"
	}
	return fmt.Sprintf("%T", err)
}

func toolErrorKind(kind ai.ToolErrorKind, isError bool) string {
	if !isError {
		return "none"
	}
	if kind == "" {
		return string(ai.ToolErrorKindTool)
	}
	return string(kind)
}

func (h *Hook) OnPostLLMCall(ctx context.Context, c *hooks.PostLLMCallContext) {
	if !h.ready() {
		return
	}
	base := common(c.HookMeta)
	base = append(base, attribute.String("model", c.Model), attribute.String("provider", c.Provider))
	h.metrics.llmDuration.Record(ctx, c.Duration.Seconds(), attrs(base...))
	if c.TimeToFirstToken > 0 {
		h.metrics.llmTTFT.Record(ctx, c.TimeToFirstToken.Seconds(), attrs(base...))
	}
	h.metrics.llmCalls.Add(ctx, 1, attrs(append(base, attribute.String("stop_reason", string(c.StopReason)), attribute.String("error.type", errorType(c.Error)))...))
	if c.Attempts > 0 {
		h.metrics.llmAttempts.Add(ctx, int64(c.Attempts), attrs(base...))
	}
	for kind, value := range map[string]int64{"input": int64(c.Usage.InputTokens), "output": int64(c.Usage.OutputTokens), "cache_read": int64(c.Usage.CacheRead), "cache_write": int64(c.Usage.CacheWrite)} {
		if value > 0 {
			h.metrics.llmTokens.Add(ctx, value, attrs(append(base, attribute.String("type", kind))...))
		}
	}
	if c.Usage.CostConfigured && c.Usage.Cost.Total > 0 {
		h.metrics.llmCost.Add(ctx, c.Usage.Cost.Total, attrs(base...))
	}
}

func (h *Hook) OnPostToolCall(ctx context.Context, c *hooks.PostToolCallContext) {
	if !h.ready() {
		return
	}
	base := common(c.HookMeta)
	base = append(base, attribute.String("tool", c.ToolName), attribute.Bool("is_error", c.IsError), attribute.String("error_kind", toolErrorKind(c.ErrorKind, c.IsError)))
	h.metrics.toolDuration.Record(ctx, c.Duration.Seconds(), attrs(base...))
	h.metrics.toolCalls.Add(ctx, 1, attrs(base...))
}

func (h *Hook) OnPostAgentCall(ctx context.Context, c *hooks.PostAgentCallContext) {
	if !h.ready() {
		return
	}
	base := common(c.HookMeta)
	h.metrics.turnDuration.Record(ctx, c.Duration.Seconds(), attrs(base...))
	h.metrics.agentTurns.Add(ctx, 1, attrs(append(base, attribute.String("error.type", errorType(c.Error)))...))
}

func (h *Hook) OnPostMemoryCall(ctx context.Context, c *hooks.PostMemoryCallContext) {
	if !h.ready() {
		return
	}
	base := common(c.HookMeta)
	base = append(base, attribute.String("op", string(c.Op)))
	h.metrics.memoryDuration.Record(ctx, c.Duration.Seconds(), attrs(base...))
}

var (
	_ hooks.HookPlugin         = (*Hook)(nil)
	_ hooks.PostLLMCallHook    = (*Hook)(nil)
	_ hooks.PostToolCallHook   = (*Hook)(nil)
	_ hooks.PostAgentCallHook  = (*Hook)(nil)
	_ hooks.PostMemoryCallHook = (*Hook)(nil)
)
