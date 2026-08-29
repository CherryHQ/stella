package sandbox

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/observability"
)

var sandboxTracer = otel.Tracer("stella/sandbox")

func recordSandboxError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	observability.RecordSpanError(span, err, "sandbox operation failed")
}
