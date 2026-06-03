// Package observability owns the process-global OpenTelemetry tracer provider.
// It is initialized once during server startup and shut down once at exit, so
// tracing is server-level infrastructure rather than a toggleable plugin.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset (or OTEL_SDK_DISABLED=true), Init is
// a no-op: the global provider stays the SDK default (no-op), and Shutdown does
// nothing. Exporter transport details (protocol, headers, TLS) are resolved
// by the OTel exporter from its standard environment variables.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds tracer settings derived from standard OTel environment
// variables. Exporter-level vars (endpoint, protocol, headers, TLS) are
// consumed by the auto exporter and are not duplicated here.
type Config struct {
	Enabled     bool   // true when an endpoint is set and the SDK is not disabled
	ServiceName string // OTel service name, defaults to "stella"
}

// LoadConfig reads OTel settings from the environment. Tracing is enabled only
// when an OTLP endpoint is configured and OTEL_SDK_DISABLED is not "true" — the
// latter is the standard kill switch operators can use to silence all telemetry
// even when an endpoint is present.
func LoadConfig() Config {
	cfg := Config{
		Enabled:     os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" && os.Getenv("OTEL_SDK_DISABLED") != "true",
		ServiceName: "stella",
	}
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	return cfg
}

// Provider is the lifecycle handle for the global tracer provider. The zero
// value (and any Provider returned when OTel is disabled) is a valid no-op:
// Shutdown returns nil without touching anything.
type Provider struct {
	tp *sdktrace.TracerProvider // nil when OTel is disabled
}

// Init loads OTel config and, when an exporter endpoint is configured, builds
// the tracer provider, installs it as the global provider, and returns a
// Provider whose Shutdown flushes pending spans. When disabled it returns a
// no-op Provider and leaves the global provider as the SDK default.
func Init(ctx context.Context) (*Provider, error) {
	cfg := LoadConfig()
	if !cfg.Enabled {
		return &Provider{}, nil
	}

	tp, err := newTracerProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(tp)
	slog.Info("otel tracing enabled",
		"endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		"service", cfg.ServiceName)
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" {
		slog.Warn("otel exporter transport is insecure; spans (including tool commands and results) are sent without TLS")
	}
	return &Provider{tp: tp}, nil
}

func newTracerProvider(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: create exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel: create resource: %w", err)
	}

	// No WithSampler: Stella uses the SDK default ParentBased(AlwaysSample).
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	), nil
}

// Shutdown flushes pending spans and stops the provider. It is a no-op when
// OTel is disabled. The caller controls the deadline via ctx.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.tp == nil {
		return nil
	}
	return p.tp.Shutdown(ctx)
}
