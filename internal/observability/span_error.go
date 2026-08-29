package observability

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// RecordSpanError records only a closed error class on a span. Error messages
// can contain upstream bodies, commands, URLs, or prompt fragments, so they
// stay in logs and never become exported span status text or exception events.
func RecordSpanError(span trace.Span, err error, status string) {
	if span == nil || err == nil {
		return
	}
	span.SetStatus(codes.Error, status)
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}
