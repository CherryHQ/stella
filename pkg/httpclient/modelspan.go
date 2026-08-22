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
// It only acts on requests whose context carries an ai.ModelRequest, so the
// shared client stays untouched for every other caller.
type modelSpanTransport struct{ base http.RoundTripper }

// RoundTrip spans one attempt. The span ends when the response headers are
// back, not when the body is drained: for a streaming call the body is the
// whole model response, so a span that spanned it would just restate the
// parent gen_ai.chat duration. What this measures is the request itself —
// connect, send, first byte — which is where a retry storm shows up.
func (t modelSpanTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	mr := ai.ModelRequestFrom(req.Context())
	if mr == nil {
		return t.base.RoundTrip(req)
	}

	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", mr.Model),
		attribute.Int("gen_ai.request.attempt", mr.NextAttempt()),
		attribute.String("http.request.method", req.Method),
	}
	if req.URL != nil {
		// Host only. A full URL can carry the API key in the query string on
		// some gateways, and spans are exported off-box.
		attrs = append(attrs, attribute.String("server.address", req.URL.Host))
	}
	ctx, span := otel.Tracer("stella").Start(req.Context(), "gen_ai.chat.request",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	defer span.End()

	resp, err := t.base.RoundTrip(req.WithContext(ctx))
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
