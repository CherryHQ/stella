package docker

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("anna/sandbox/docker")

func sessionTraceAttrs(sessionID string, policy Policy, image, workspaceHost string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("anna.sandbox.backend", "docker"),
		attribute.String("anna.sandbox.session.id", sessionID),
		attribute.String("anna.sandbox.image", image),
		attribute.String("anna.sandbox.workspace_host", workspaceHost),
		attribute.String("anna.sandbox.network.mode", string(policy.NetworkModeOrDefault())),
		attribute.Int("anna.sandbox.readonly_dir_count", len(policy.Filesystem.ReadOnlyPaths)),
	}
}

func recordError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}
