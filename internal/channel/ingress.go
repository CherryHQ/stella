package channel

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type ingressContextKey struct{}

type ingressSpan struct {
	span      trace.Span
	startedAt time.Time
	queuedAt  time.Time
	mu        sync.Mutex
	once      sync.Once
}

func startIngress(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, *ingressSpan) {
	ctx, span := tracer().Start(ctx, name, trace.WithAttributes(attrs...))
	info := &ingressSpan{span: span, startedAt: time.Now()}
	return context.WithValue(ctx, ingressContextKey{}, info), info
}

func tracer() trace.Tracer { return otel.Tracer("stella") }

func markIngressQueued(ctx context.Context) {
	info, _ := ctx.Value(ingressContextKey{}).(*ingressSpan)
	if info == nil {
		return
	}
	info.mu.Lock()
	if info.queuedAt.IsZero() {
		info.queuedAt = time.Now()
	}
	info.mu.Unlock()
}

func finishIngress(ctx context.Context) {
	info, _ := ctx.Value(ingressContextKey{}).(*ingressSpan)
	if info == nil {
		return
	}
	info.once.Do(func() {
		info.mu.Lock()
		queuedAt := info.queuedAt
		startedAt := info.startedAt
		info.mu.Unlock()
		if queuedAt.IsZero() {
			queuedAt = startedAt
		}
		info.span.SetAttributes(attribute.Float64("stella.ingress.queue_wait_s", time.Since(queuedAt).Seconds()))
		info.span.End()
	})
}
