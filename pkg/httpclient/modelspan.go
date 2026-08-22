package httpclient

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/ai"
)

// modelSpanTransport gives every provider HTTP request its own span. Provider
// SDKs retry inside a single Stream call, so without this the whole logical
// call is one opaque span and a retried request is invisible.
//
// A model request bypasses the otelhttp instrumentation entirely and goes to
// plain. Two reasons, both hard requirements:
//   - otelhttp records url.full, and a provider URL can carry the API key in
//     its query string.
//   - otelhttp's client span lives until the response body hits EOF, so a
//     streamed reply produced a child that outlived its parent and reported
//     success for a 2xx whose stream later broke. One model request gets one
//     span with one meaning.
type modelSpanTransport struct {
	// next handles everything that is not a model request; it carries the
	// otelhttp instrumentation when tracing is on.
	next http.RoundTripper
	// plain is the uninstrumented base, used for model requests.
	plain http.RoundTripper
	// tracing mirrors otelEnabled() at construction time. It is the kill
	// switch: OTEL_SDK_DISABLED must silence these spans even if something
	// else in the process installed a real tracer provider.
	tracing bool
}

// RoundTrip spans one attempt. The span ends when the response headers are
// back, not when the body is drained: for a streaming call the body is the
// whole model response, so a span that spanned it would just restate the
// parent gen_ai.chat duration. What this measures is the request itself —
// connect, send, first byte — which is where a retry storm shows up.
func (t modelSpanTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	mr := ai.ModelRequestFrom(req.Context())
	if mr == nil {
		return t.next.RoundTrip(req)
	}
	// Counting is free and useful without an exporter, so it happens either
	// way. Everything below it allocates, so tracing-off stops here: no
	// attributes, no span, no request clone.
	attempt := mr.NextAttempt()
	if !t.tracing {
		return t.plain.RoundTrip(req)
	}

	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", mr.Model),
		attribute.Int("gen_ai.request.attempt", attempt),
		attribute.String("http.request.method", req.Method),
	}
	if req.URL != nil {
		// Host only. A full URL can carry the API key in the query string, and
		// spans are exported off-box.
		attrs = append(attrs, attribute.String("server.address", req.URL.Host))
	}
	ctx, span := otel.Tracer("stella").Start(req.Context(), "gen_ai.chat.request",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	defer span.End()

	resp, err := t.plain.RoundTrip(req.WithContext(ctx))
	if err != nil {
		// The error string is a *url.Error carrying the full request URL, so
		// only the Go type goes on the span.
		span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
		span.SetStatus(codes.Error, "provider request failed")
		return resp, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
	}
	return resp, nil
}
