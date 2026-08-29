package metrichook

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type queueStats struct{ depth int }

func (q queueStats) QueueDepth() int     { return q.depth }
func (q queueStats) DroppedCount() int64 { return 0 }

func collect(t *testing.T, reader *metric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	return rm
}

func metricNames(rm metricdata.ResourceMetrics) map[string]metricdata.Metrics {
	out := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func assertNoForbiddenLabels(t *testing.T, rm metricdata.ResourceMetrics) {
	t.Helper()
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			check := func(set attribute.Set) {
				for _, kv := range set.ToSlice() {
					if kv.Key == "session_id" || kv.Key == "user_id" {
						t.Errorf("metric %q contains forbidden label %q", m.Name, kv.Key)
					}
				}
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					check(dp.Attributes)
				}
			case metricdata.Sum[float64]:
				for _, dp := range data.DataPoints {
					check(dp.Attributes)
				}
			case metricdata.Histogram[int64]:
				for _, dp := range data.DataPoints {
					check(dp.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					check(dp.Attributes)
				}
			case metricdata.Gauge[int64]:
				for _, dp := range data.DataPoints {
					check(dp.Attributes)
				}
			case metricdata.Gauge[float64]:
				for _, dp := range data.DataPoints {
					check(dp.Attributes)
				}
			}
		}
	}
}

func TestHookRecordsCoreInstrumentsAndBoundedLabels(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	q := queueStats{depth: 7}
	h := New(q, func() int64 { return 3 })
	if err := h.Bind(provider.Meter("stella")); err != nil {
		t.Fatal(err)
	}
	h.OnPostAgentCall(context.Background(), &hooks.PostAgentCallContext{
		HookMeta: hooks.HookMeta{AgentID: "agent-uuid", UserID: "user", SessionID: "session", Channel: "web"}, Duration: time.Second,
	})
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: hooks.HookMeta{AgentID: "agent-uuid", UserID: "user", SessionID: "session", Channel: "telegram"},
		Model:    "model", Provider: "provider", Duration: 2 * time.Second, TimeToFirstToken: time.Second,
		Usage: ai.Usage{
			Reported: true, InputTokens: 4, OutputTokens: 5, CacheRead: 6, CacheWrite: 7,
			CostConfigured: true, Cost: ai.UsageCost{Total: 0.25},
		}, StopReason: ai.StopReasonStop, Attempts: 2,
	})
	h.OnPostToolCall(context.Background(), &hooks.PostToolCallContext{
		HookMeta: hooks.HookMeta{AgentID: "agent-uuid", UserID: "user", SessionID: "session", Channel: "feishu"},
		ToolName: "bash", IsError: true, ErrorKind: ai.ToolErrorKindCommandNonzero, Duration: time.Second,
	})
	h.OnPostMemoryCall(context.Background(), &hooks.PostMemoryCallContext{HookMeta: hooks.HookMeta{UserID: "user", SessionID: "session"}, Op: hooks.MemoryOpSearch, Duration: time.Second})
	h.RecordQueueDrop()

	rm := collect(t, reader)
	names := metricNames(rm)
	for _, name := range []string{
		"stella.llm.call.duration", "stella.llm.time_to_first_token", "stella.llm.tokens", "stella.llm.cost",
		"stella.llm.calls", "stella.llm.attempts", "stella.tool.call.duration", "stella.tool.calls",
		"stella.agent.turn.duration", "stella.agent.turns", "stella.memory.op.duration",
		"stella.llm_usage.queue.dropped", "stella.llm_usage.queue.depth", "stella.trace.sessions.active",
	} {
		if _, ok := names[name]; !ok {
			t.Errorf("missing metric %q", name)
		}
	}
	assertNoForbiddenLabels(t, rm)
}

func TestHookBindIsExplicitAndOnlyOnce(t *testing.T) {
	h := New(nil, nil)
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	meter := provider.Meter("stella")
	if err := h.Bind(meter); err != nil {
		t.Fatal(err)
	}
	if err := h.Bind(meter); err == nil {
		t.Fatal("second Bind succeeded")
	}
	// An unbound hook is deliberately inert, not a panic waiting to happen.
	New(nil, nil).OnPostMemoryCall(context.Background(), &hooks.PostMemoryCallContext{Duration: time.Second})
}
