package boxsh

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

var tracer = otel.Tracer("anna/sandbox/boxsh")

func sessionTraceAttrs(sessionID string, policy Policy, cfg boxshclient.BackendConfig) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("anna.sandbox.backend", "boxsh"),
		attribute.String("anna.sandbox.session.id", sessionID),
		attribute.String("anna.sandbox.user_root", cfg.UserRoot),
		attribute.String("anna.sandbox.work_dir", cfg.WorkDir),
		attribute.String("anna.sandbox.network.mode", cfg.Sandbox.ModeOrDefault()),
		attribute.Int("anna.sandbox.readonly_dir_count", len(cfg.ReadOnlyDirs)),
		attribute.Bool("anna.sandbox.relaxed", policy.Relaxed),
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
