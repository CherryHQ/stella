package httpclient

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/ai"
)

// clientSpanTransport spans every outbound request from this package's clients,
// and it is the reason none of them can leak a credential into a trace.
//
// Two layers, deliberately separate:
//
//   - Safety is unconditional and belongs here. Every request records the host
//     and nothing else of the URL — no path, no query, where a provider gateway
//     puts the API key — and a transport error contributes its Go type, never
//     its message, which carries the URL back. This holds for callers that know
//     nothing about tracing, which is the point: an opt-in marker would make
//     credential safety depend on every call site remembering it.
//   - gen_ai semantics are additive. A request whose context carries an
//     ai.ModelRequest also gets the attempt number and the chat attributes, and
//     its span is named gen_ai.chat.request. Forgetting the marker costs a
//     span's meaning, never a secret.
//
// This replaces otelhttp for outbound requests: otelhttp records url.full and
// the raw transport error, and its client span stays open until the response
// body reaches EOF, which for a streamed model reply is the entire response.
// W3C context is still propagated, so downstream services keep the trace.
type clientSpanTransport struct {
	base http.RoundTripper
	// tracing mirrors otelenv.TracesEnabled() at construction. It is the kill
	// switch: OTEL_SDK_DISABLED must silence these spans even if something
	// else in the process installed a real tracer provider.
	tracing bool
}

// RoundTrip spans one request. The span ends when the response headers are
// back, not when the body is drained: for a streaming call the body is the
// whole model response, so a span that spanned it would just restate the
// parent gen_ai.chat duration. What this measures is the request itself —
// connect, send, first byte — which is where a retry storm shows up.
func (t clientSpanTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	mr := ai.ModelRequestFrom(req.Context())
	// Counting is free and useful without an exporter (it feeds the logs and
	// the agent's hook payload), so it happens either way. Everything below
	// allocates, so tracing-off stops here.
	attempt := 0
	if mr != nil {
		attempt = mr.NextAttempt()
	}
	if !t.tracing {
		return t.base.RoundTrip(req)
	}

	name := "HTTP " + req.Method
	attrs := []attribute.KeyValue{attribute.String("http.request.method", req.Method)}
	if req.URL != nil {
		attrs = append(attrs, attribute.String("server.address", req.URL.Host))
	}
	if mr != nil {
		name = "gen_ai.chat.request"
		attrs = append(attrs,
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.request.model", mr.Model),
			attribute.Int("gen_ai.request.attempt", attempt),
		)
	}

	ctx, span := otel.Tracer("stella").Start(req.Context(), name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	defer span.End()

	// Clone before touching headers: the caller's request must not gain a
	// traceparent from a shallow copy sharing its header map.
	outbound := req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(outbound.Header))

	resp, err := t.base.RoundTrip(outbound)
	if err != nil {
		// The error is a *url.Error carrying the full request URL, so only the
		// Go type goes on the span.
		span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
		span.SetStatus(codes.Error, "http request failed")
		return resp, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
	}
	return resp, nil
}
