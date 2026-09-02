package observability

import (
	"net/http"
	"regexp"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Handler wraps h with OpenTelemetry HTTP server instrumentation so every
// inbound request produces a server span. It is always applied: when tracing
// is disabled the global provider is a no-op, so wrapping adds negligible
// overhead and keeps the server's handler composition unconditional.
//
// The span name formatter gives each span a "<METHOD> <templated-path>" name.
// Path segments that look like resource instance IDs are collapsed to a
// placeholder (see templatePath) so /api/projects/123 and /api/projects/456
// share one span name — span names are an aggregation key, and raw instance
// paths would explode cardinality in the tracing backend.
func Handler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + templatePath(r.URL.Path)
		}),
	)
}

// reHighCardinalitySegment matches path segments that are almost certainly
// resource identifiers rather than static route names: numeric IDs, UUIDs, and
// long hex/opaque tokens.
var reHighCardinalitySegment = regexp.MustCompile(`^(\d+|[0-9a-fA-F]{8}-[0-9a-fA-F-]{27,}|[0-9a-fA-F]{16,}|[A-Za-z0-9_-]{21,})$`)

// templatePath replaces high-cardinality path segments with ":id" so span
// names stay bounded. It is a heuristic — without router templates we cannot
// know the true parameter names, but collapsing ID-shaped segments keeps span
// cardinality proportional to the route count, not the data set.
func templatePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if reHighCardinalitySegment.MatchString(s) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}
