package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTraceContextHandlerAddsIDs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewTraceContextHandler(slog.NewTextHandler(&buf, nil)))

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01, 0x02},
		SpanID:  trace.SpanID{0x03, 0x04},
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	log.InfoContext(ctx, "with span")
	log.Info("without span")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "trace_id="+sc.TraceID().String()) ||
		!strings.Contains(lines[0], "span_id="+sc.SpanID().String()) {
		t.Errorf("span-carrying record missing trace/span ids: %q", lines[0])
	}
	if strings.Contains(lines[1], "trace_id") {
		t.Errorf("context-free record must not carry a trace_id: %q", lines[1])
	}
}
