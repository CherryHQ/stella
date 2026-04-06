// Package httpclient provides a shared resty HTTP client factory with
// optional OpenTelemetry tracing. OTel transport instrumentation activates
// only when OTEL_EXPORTER_OTLP_ENDPOINT is set, aligned with the trace
// hook in plugins/hooks/trace/.
package httpclient

import (
	"net/http"
	"os"
	"time"

	"github.com/go-resty/resty/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const defaultTimeout = 30 * time.Second

// otelEnabled reports whether the OTel exporter endpoint is configured.
func otelEnabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

// transport returns an http.RoundTripper, wrapping http.DefaultTransport
// with otelhttp instrumentation when OTel is active.
func transport() http.RoundTripper {
	base := http.DefaultTransport
	if otelEnabled() {
		return otelhttp.NewTransport(base)
	}
	return base
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

// StdHTTPClient returns a standard *http.Client with OTel transport
// instrumentation. No timeout is set because this client is used for
// SSE/streaming requests where the context deadline controls cancellation.
// otelhttp resolves the TracerProvider from the request context at call
// time (not construction time), so it works regardless of init order.
func StdHTTPClient() *http.Client {
	return &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}
