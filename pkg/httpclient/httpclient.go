// Package httpclient provides a shared resty HTTP client factory with
// optional OpenTelemetry tracing. Whether to instrument is decided by package
// otelenv, the same predicate the tracer provider in package observability
// uses, so a transport can never emit spans the provider is not exporting (or
// stay silent while it is).
package httpclient

import (
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/CherryHQ/stella/pkg/otelenv"
)

const defaultTimeout = 30 * time.Second

// transport returns the instrumented http.RoundTripper every client in this
// package uses. It is always installed: it counts provider retry attempts with
// or without export, and when tracing is on it is the only thing that spans
// outbound requests — see clientSpanTransport for why not otelhttp.
func transport() http.RoundTripper {
	return clientSpanTransport{base: http.DefaultTransport, tracing: otelenv.TracesEnabled()}
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

// StdHTTPClient returns a standard *http.Client with the shared instrumented
// transport. No timeout is set because this client is used for SSE/streaming
// requests where the context deadline controls cancellation.
func StdHTTPClient() *http.Client {
	return &http.Client{
		Transport: transport(),
	}
}
