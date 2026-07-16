package observability

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"

	"go.opentelemetry.io/otel/trace"
)

// NewTraceContextHandler wraps next so records logged with a span-carrying
// context (the slog *Context variants) gain trace_id/span_id attributes. The
// OTLP log bridge extracts trace context natively; this wrapper gives the
// console output the same correlation, so an operator can jump from a stderr
// line to the trace in their backend. Records logged without a context pass
// through unchanged.
func NewTraceContextHandler(next slog.Handler) slog.Handler {
	return traceContextHandler{next: next}
}

type traceContextHandler struct {
	next slog.Handler
}

func (h traceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h traceContextHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		record = record.Clone()
		record.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.next.Handle(ctx, record)
}

func (h traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceContextHandler{next: h.next.WithAttrs(attrs)}
}

func (h traceContextHandler) WithGroup(name string) slog.Handler {
	return traceContextHandler{next: h.next.WithGroup(name)}
}

type teeHandler struct {
	handlers []slog.Handler
}

func currentSlogHandler() slog.Handler {
	handler := slog.Default().Handler()
	// The package-level default handler writes through the standard log package.
	// slog.SetDefault redirects that package back into slog, so teeing it would
	// recurse. stellad installs its own TextHandler before Init; this fallback is
	// for tests and direct package use.
	if reflect.TypeOf(handler).String() == "*slog.defaultHandler" {
		return slog.NewTextHandler(os.Stderr, nil)
	}
	return handler
}

func newTeeHandler(handlers ...slog.Handler) slog.Handler {
	return teeHandler{handlers: handlers}
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			err = errors.Join(err, handler.Handle(ctx, record.Clone()))
		}
	}
	return err
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return teeHandler{handlers: handlers}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return teeHandler{handlers: handlers}
}
