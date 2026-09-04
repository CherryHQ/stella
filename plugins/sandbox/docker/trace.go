package docker

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

var tracer = otel.Tracer("stella/sandbox/docker")

func sessionTraceAttrs(sessionID string, policy sandboxpkg.Policy, image, workspaceHost string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("stella.sandbox.backend", "docker"),
		attribute.String("stella.sandbox.session.id", sessionID),
		attribute.String("stella.sandbox.image", image),
		attribute.String("stella.sandbox.workspace_host", workspaceHost),
		attribute.String("stella.sandbox.network.mode", string(policy.NetworkModeOrDefault())),
	}
}

func recordError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.SetStatus(codes.Error, "sandbox operation failed")
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}
