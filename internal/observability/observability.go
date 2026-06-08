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
	"errors"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	otellog "go.opentelemetry.io/otel/log"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Config holds OTel settings derived from standard environment variables.
// Exporter-level vars (endpoint, protocol, headers, TLS) are consumed by the
// auto exporters and are not duplicated here.
type Config struct {
	Enabled       bool   // legacy alias for TracesEnabled
	TracesEnabled bool   // true when trace export is configured and the SDK is not disabled
	LogsEnabled   bool   // true when log export is configured and the SDK is not disabled
	ServiceName   string // OTel service name, defaults to "stella"
}

// LoadConfig reads OTel settings from the environment. Tracing is enabled when
// the span exporter has something to export to — either an OTLP endpoint (the
// generic or traces-specific variable) or an explicit OTEL_TRACES_EXPORTER such
// as "console". Logs follow the equivalent generic/logs endpoint and
// OTEL_LOGS_EXPORTER settings. OTEL_SDK_DISABLED=true silences every signal;
// OTEL_TRACES_EXPORTER=none and OTEL_LOGS_EXPORTER=none silence their respective
// signals even when a generic endpoint is present.
func LoadConfig() Config {
	tracesExporter := os.Getenv("OTEL_TRACES_EXPORTER")
	logsExporter := os.Getenv("OTEL_LOGS_EXPORTER")
	genericEndpointSet := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
	tracesEndpointSet := genericEndpointSet || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
	logsEndpointSet := genericEndpointSet || os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") != ""
	disabled := os.Getenv("OTEL_SDK_DISABLED") == "true"
	tracesEnabled := !disabled && tracesExporter != "none" && (tracesEndpointSet || tracesExporter != "")

	cfg := Config{
		Enabled:       tracesEnabled,
		TracesEnabled: tracesEnabled,
		LogsEnabled:   !disabled && logsExporter != "none" && (logsEndpointSet || logsExporter != ""),
		ServiceName:   "stella",
	}
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	return cfg
}

// Provider is the lifecycle handle for global OTel providers. The zero value
// (and any Provider returned when OTel is disabled) is a valid no-op: Shutdown
// returns nil without touching anything.
type Provider struct {
	tp *sdktrace.TracerProvider // nil when trace export is disabled
	lp *sdklog.LoggerProvider   // nil when log export is disabled

	previousTracerProvider trace.TracerProvider
	previousLoggerProvider otellog.LoggerProvider
	previousSlog           *slog.Logger
}

// Init loads OTel config and, when an exporter endpoint is configured, builds
// the enabled providers, installs them as globals, and returns a Provider whose
// Shutdown flushes pending telemetry. When disabled it returns a no-op Provider
// and leaves the global providers as the SDK defaults.
func Init(ctx context.Context) (*Provider, error) {
	cfg := LoadConfig()
	if !cfg.TracesEnabled && !cfg.LogsEnabled {
		return &Provider{}, nil
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, err
	}

	p := &Provider{}
	if cfg.TracesEnabled {
		p.tp, err = newTracerProvider(ctx, res)
		if err != nil {
			return nil, err
		}
	}
	if cfg.LogsEnabled {
		p.lp, err = newLoggerProvider(ctx, res)
		if err != nil {
			return nil, err
		}
	}

	if p.tp != nil {
		p.previousTracerProvider = otel.GetTracerProvider()
		otel.SetTracerProvider(p.tp)
		slog.Info("otel tracing enabled",
			"endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			"service", cfg.ServiceName)
	}
	if p.lp != nil {
		p.previousLoggerProvider = otellogglobal.GetLoggerProvider()
		p.previousSlog = slog.Default()
		otellogglobal.SetLoggerProvider(p.lp)
		slog.SetDefault(slog.New(newTeeHandler(currentSlogHandler(), otelslog.NewHandler("stella"))))
		slog.Info("otel logs enabled",
			"endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			"service", cfg.ServiceName)
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" {
		slog.Warn("otel exporter transport is insecure; telemetry is sent without TLS")
	}
	return p, nil
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel: create resource: %w", err)
	}
	return res, nil
}

func newTracerProvider(ctx context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: create span exporter: %w", err)
	}

	// No WithSampler: Stella uses the SDK default ParentBased(AlwaysSample).
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	), nil
}

func newLoggerProvider(ctx context.Context, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: create log exporter: %w", err)
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	), nil
}

// Shutdown flushes pending telemetry and stops the providers. It is a no-op
// when OTel is disabled. The caller controls the deadline via ctx.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var err error
	if p.lp != nil {
		if p.previousSlog != nil {
			slog.SetDefault(p.previousSlog)
		}
		if p.previousLoggerProvider != nil {
			otellogglobal.SetLoggerProvider(p.previousLoggerProvider)
		}
		err = errors.Join(err, p.lp.Shutdown(ctx))
	}
	if p.tp != nil {
		if p.previousTracerProvider != nil {
			otel.SetTracerProvider(p.previousTracerProvider)
		}
		err = errors.Join(err, p.tp.Shutdown(ctx))
	}
	return err
}
