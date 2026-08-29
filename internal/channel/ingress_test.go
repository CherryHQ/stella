package channel

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestIngressSpanEndsAfterQueueAdmissionBoundary(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })

	ctx, _ := startIngress(context.Background(), "channel.ingress", attribute.String("stella.channel.name", "web"))
	markIngressQueued(ctx)
	time.Sleep(time.Millisecond)
	finishIngress(ctx)
	spans := tracetest.SpanStubsFromReadOnlySpans(recorder.Ended())
	if len(spans) != 1 || spans[0].Name != "channel.ingress" {
		t.Fatalf("spans = %#v", spans)
	}
	value, ok := spanAttribute(spans[0], "stella.ingress.queue_wait_s")
	if !ok || value.AsFloat64() < 0 {
		t.Fatalf("queue wait = %v (ok=%v)", value, ok)
	}
	finishIngress(ctx)
	if len(recorder.Ended()) != 1 {
		t.Fatal("ingress span ended more than once")
	}
}

func spanAttribute(span tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}
