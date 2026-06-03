package observability

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Handler wraps h with OpenTelemetry HTTP server instrumentation so every
// inbound request produces a server span. It is always applied: when tracing
// is disabled the global provider is a no-op, so wrapping adds negligible
// overhead and keeps the server's handler composition unconditional.
//
// The span name formatter gives each span a "<METHOD> <path>" name instead of
// the static "http.server", so traces are distinguishable per route.
func Handler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}
