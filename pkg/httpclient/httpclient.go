// Package httpclient provides a shared resty HTTP client factory with
// optional OpenTelemetry tracing. OTel transport instrumentation activates
// only when OTEL_EXPORTER_OTLP_ENDPOINT is set and OTEL_SDK_DISABLED is not
// true, aligned with the global tracer provider in package observability.
package httpclient

import (
	"net/http"
	"os"
	"time"

	"github.com/go-resty/resty/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const defaultTimeout = 30 * time.Second

// otelEnabled reports whether tracing should install HTTP transport spans.
func otelEnabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" && os.Getenv("OTEL_SDK_DISABLED") != "true"
}

// transport returns an http.RoundTripper, wrapping http.DefaultTransport
// with otelhttp instrumentation when OTel is active.
func transport() http.RoundTripper {
	base := http.DefaultTransport
	tracing := otelEnabled()
	next := base
	if tracing {
		next = otelhttp.NewTransport(base)
	}
	// Always outermost: it counts provider retry attempts, which the agent
	// loop reports whether or not spans are being exported, and it keeps
	// model requests away from otelhttp (see modelSpanTransport).
	return modelSpanTransport{next: next, plain: base, tracing: tracing}
}

// New creates a resty client with the default 30s timeout and optional
// OTel transport instrumentation.
func New() *resty.Client {
	return resty.NewWithClient(&http.Client{
		Transport: transport(),
		Timeout:   defaultTimeout,
	})
}

// NewWithTimeout creates a resty client with a custom timeout and optional
// OTel transport instrumentation.
func NewWithTimeout(timeout time.Duration) *resty.Client {
	return resty.NewWithClient(&http.Client{
		Transport: transport(),
		Timeout:   timeout,
	})
}

// StdHTTPClient returns a standard *http.Client with optional OTel transport
// instrumentation. No timeout is set because this client is used for
// SSE/streaming requests where the context deadline controls cancellation.
// When enabled, otelhttp resolves the TracerProvider from the request context
// at call time (not construction time), so it works regardless of init order.
func StdHTTPClient() *http.Client {
	return &http.Client{
		Transport: transport(),
	}
}
