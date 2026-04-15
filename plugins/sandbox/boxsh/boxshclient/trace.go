package boxshclient

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("anna/sandbox/boxshclient")

func commonTraceAttrs(cfg SessionConfig) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("anna.sandbox.backend", "boxsh"),
		attribute.String("anna.sandbox.src", cfg.Src),
		attribute.String("anna.sandbox.dst", cfg.Dst),
		attribute.String("anna.sandbox.cwd", cfg.Cwd),
		attribute.Int("anna.sandbox.readonly_dir_count", len(uniqueCleanAbsPaths(cfg.ReadOnlyDirs))),
		attribute.String("anna.sandbox.network.mode", cfg.NetworkMode),
	}
}

func recordTraceError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}
