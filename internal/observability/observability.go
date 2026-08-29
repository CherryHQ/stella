// Package observability owns the process-global OpenTelemetry providers (tracer
// and logger). They are initialized once during server startup and shut down
// once at exit, so telemetry is server-level infrastructure rather than a
// toggleable plugin.
//
// When no OTLP endpoint or signal-specific exporter is configured (or
// OTEL_SDK_DISABLED=true), Init is a no-op: the global providers stay the SDK
// defaults (no-op), and Shutdown does nothing. Exporter transport details
// (protocol, headers, TLS) are resolved by the OTel exporters from their
// standard environment variables.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	otellog "go.opentelemetry.io/otel/log"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/diagnostic"
	"github.com/CherryHQ/stella/internal/version"
	"github.com/CherryHQ/stella/pkg/otelenv"
)

// Config holds OTel settings derived from standard environment variables.
// Exporter-level vars (endpoint, protocol, headers, TLS) are consumed by the
// auto exporters and are not duplicated here.
type Config struct {
	Enabled     bool   // true when trace export is configured and the SDK is not disabled
	ServiceName string // OTel service name, defaults to "stella"
}

type errorRateLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (r *errorRateLimiter) allow(signal string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		r.last = make(map[string]time.Time)
	}
	if previous := r.last[signal]; now.Sub(previous) < time.Minute {
		return false
	}
	r.last[signal] = now
	return true
}

func otelErrorSignal(err error) string {
	message := strings.ToLower(err.Error())
	for _, signal := range []string{"logs", "traces", "metrics"} {
		if strings.Contains(message, "/v1/"+signal) || strings.Contains(message, signal+" exporter") {
			return signal
		}
	}
	return "otel"
}

// LoadConfig reads OTel settings from the environment. Whether a signal is
// exporting is decided by package otelenv, which is also what the HTTP
// transport asks before it emits provider spans — one answer, one place.
func LoadConfig() Config {
	cfg := Config{
		Enabled:     otelenv.TracesEnabled(),
		ServiceName: "stella",
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
	mp *sdkmetric.MeterProvider // nil when metric export is disabled

	previousTracerProvider trace.TracerProvider
	previousLoggerProvider otellog.LoggerProvider
	previousMeterProvider  otelmetric.MeterProvider
	previousPropagator     propagation.TextMapPropagator
	previousSlog           *slog.Logger
}

// Init loads OTel config and, when an exporter endpoint is configured, builds
// the enabled providers, installs them as globals, and returns a Provider whose
// Shutdown flushes pending telemetry. When disabled it returns a no-op Provider
// and leaves the global providers as the SDK defaults.
func Init(ctx context.Context) (*Provider, error) {
	cfg := LoadConfig()
	logsEnabled := otelenv.LogsEnabled()
	metricsEnabled := otelenv.MetricsEnabled()
	if !cfg.Enabled && !logsEnabled && !metricsEnabled {
		return &Provider{}, nil
	}

	// SDK export failures must bypass the OTLP leg. A collector outage must not
	// create a log -> export -> failure -> log feedback loop.
	consoleLog := slog.New(NewTraceContextHandler(currentSlogHandler()))
	setConsoleOnlyLogger(consoleLog)
	rateLimiter := &errorRateLimiter{}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		signal := otelErrorSignal(err)
		if rateLimiter.allow(signal, time.Now()) {
			consoleLog.Warn("otel SDK error", "component", "otel", "signal", signal, "error", err)
		}
	}))

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, err
	}

	p := &Provider{}
	if cfg.Enabled {
		p.tp, err = newTracerProvider(ctx, res)
		if err != nil {
			return nil, exporterInitError("trace", traceEndpoint(), err)
		}
	}
	if logsEnabled {
		p.lp, err = newLoggerProvider(ctx, res)
		if err != nil {
			_ = p.shutdownProviders(ctx)
			return nil, exporterInitError("log", logsEndpoint(), err)
		}
	}
	if metricsEnabled {
		p.mp, err = newMeterProvider(ctx, res)
		if err != nil {
			_ = p.shutdownProviders(ctx)
			return nil, exporterInitError("metric", metricsEndpoint(), err)
		}
	}

	if p.tp != nil {
		p.previousTracerProvider = otel.GetTracerProvider()
		otel.SetTracerProvider(p.tp)
		// The SDK's default propagator is a no-op, so without this every
		// outbound request leaves without a traceparent and every inbound one
		// starts a new trace instead of continuing the caller's. W3C plus
		// baggage is the standard pair.
		p.previousPropagator = otel.GetTextMapPropagator()
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		slog.Info("otel tracing enabled",
			"endpoint", traceEndpoint(),
			"service", cfg.ServiceName)
	}
	if p.lp != nil {
		p.previousLoggerProvider = otellogglobal.GetLoggerProvider()
		p.previousSlog = slog.Default()
		otellogglobal.SetLoggerProvider(p.lp)
		logHandler := cli.NewMinLevelHandler(cli.ParseLogLevel(os.Getenv("LOG_LEVEL")), otelslog.NewHandler("stella"))
		slog.SetDefault(slog.New(newTeeHandler(currentSlogHandler(), logHandler)))
		slog.Info("otel logs enabled",
			"endpoint", logsEndpoint(),
			"service", cfg.ServiceName)
	}
	if p.mp != nil {
		p.previousMeterProvider = otel.GetMeterProvider()
		otel.SetMeterProvider(p.mp)
		// Go runtime metrics (memory, GC, goroutines). otelhttp picks up the
		// global meter provider by itself, so HTTP server metrics need no start
		// call. A runtime.Start failure only loses one instrument set; the
		// provider is already installed, so warn rather than fail startup.
		if err := runtime.Start(runtime.WithMeterProvider(p.mp)); err != nil {
			slog.Warn("otel runtime metrics failed to start", "error", err)
		}
		slog.Info("otel metrics enabled",
			"endpoint", metricsEndpoint(),
			"service", cfg.ServiceName)
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" {
		slog.Warn("otel exporter transport is insecure; telemetry is sent without TLS")
	}
	return p, nil
}

// exporterEndpoint returns the signal-specific endpoint when configured,
// otherwise the generic OTLP endpoint. It is only for diagnostics.
func exporterEndpoint(signalKey string) string {
	raw := os.Getenv(signalKey)
	if raw == "" {
		raw = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	return diagnostic.Endpoint(raw)
}

func traceEndpoint() string   { return exporterEndpoint("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") }
func logsEndpoint() string    { return exporterEndpoint("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") }
func metricsEndpoint() string { return exporterEndpoint("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") }

// exporterInitializationError keeps the cause available to errors.Is/As, but
// never includes it in Error: exporters may echo endpoint credentials.
type exporterInitializationError struct {
	signal   string
	endpoint string
	cause    error
}

func (e *exporterInitializationError) Error() string {
	if e.endpoint == "" {
		return fmt.Sprintf("otel: create %s exporter: initialization failed", e.signal)
	}
	return fmt.Sprintf("otel: create %s exporter for %s: initialization failed", e.signal, e.endpoint)
}

func (e *exporterInitializationError) Unwrap() error { return e.cause }

func exporterInitError(signal, safeEndpoint string, cause error) error {
	return &exporterInitializationError{signal: signal, endpoint: safeEndpoint, cause: cause}
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	// WithFromEnv is last so OTEL_RESOURCE_ATTRIBUTES (and OTEL_SERVICE_NAME)
	// override the in-code defaults; without it those variables are silently
	// dropped, since resource.New has no default detectors.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version.DisplayVersion()),
		),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
		resource.WithFromEnv(),
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

	// The SDK default is ParentBased(AlwaysSample). Operators can reduce noisy
	// DB/query spans with OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG.
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	), nil
}

func newMeterProvider(ctx context.Context, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: create metric reader: %w", err)
	}

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	), nil
}

func newLoggerProvider(ctx context.Context, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel: create log exporter: %w", err)
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(&gracefulExporter{inner: exporter})),
		sdklog.WithResource(res),
	), nil
}

// gracefulExporter wraps a log exporter and auto-disables on the first
// "Unimplemented" gRPC error, which means the backend does not support the
// logs service (e.g. Jaeger). A single warning is logged; subsequent exports
// become no-ops.
type gracefulExporter struct {
	inner    sdklog.Exporter
	disabled atomic.Bool
}

func (e *gracefulExporter) Export(ctx context.Context, records []sdklog.Record) error {
	if e.disabled.Load() {
		return nil
	}
	err := e.inner.Export(ctx, records)
	if err != nil && strings.Contains(err.Error(), "Unimplemented") {
		slog.Warn("otel log export not supported by backend, disabling log export")
		e.disabled.Store(true)
		return nil
	}
	return err
}

func (e *gracefulExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

func (e *gracefulExporter) ForceFlush(ctx context.Context) error {
	if e.disabled.Load() {
		return nil
	}
	return e.inner.ForceFlush(ctx)
}

// MetricsEnabled reports whether Init installed a real meter provider.
func (p *Provider) MetricsEnabled() bool { return p != nil && p.mp != nil }

// ForceFlushLogs makes log-export tests and controlled shutdown paths wait for
// the current batch without exposing the provider implementation.
func (p *Provider) ForceFlushLogs(ctx context.Context) error {
	if p == nil || p.lp == nil {
		return nil
	}
	return p.lp.ForceFlush(ctx)
}

// Shutdown flushes pending telemetry and stops the providers. It is a no-op
// when OTel is disabled. The caller controls the deadline via ctx.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.lp != nil {
		if p.previousSlog != nil {
			slog.SetDefault(p.previousSlog)
		}
		if p.previousLoggerProvider != nil {
			otellogglobal.SetLoggerProvider(p.previousLoggerProvider)
		}
	}
	if p.mp != nil && p.previousMeterProvider != nil {
		otel.SetMeterProvider(p.previousMeterProvider)
	}
	if p.tp != nil && p.previousTracerProvider != nil {
		otel.SetTracerProvider(p.previousTracerProvider)
	}
	if p.previousPropagator != nil {
		otel.SetTextMapPropagator(p.previousPropagator)
	}
	return p.shutdownProviders(ctx)
}

// shutdownProviders stops whichever providers were built, without touching the
// global registrations. Init uses it directly to unwind a partial build before
// any global was installed.
func (p *Provider) shutdownProviders(ctx context.Context) error {
	var err error
	if p.lp != nil {
		err = errors.Join(err, providerShutdownError("log", p.lp.Shutdown(ctx)))
	}
	if p.mp != nil {
		err = errors.Join(err, providerShutdownError("metric", p.mp.Shutdown(ctx)))
	}
	if p.tp != nil {
		err = errors.Join(err, providerShutdownError("trace", p.tp.Shutdown(ctx)))
	}
	return err
}

// providerShutdownFailure keeps the cause inspectable without exposing it in
// Error, because Shutdown callers log the returned error.
type providerShutdownFailure struct {
	signal string
	cause  error
}

func (e *providerShutdownFailure) Error() string {
	return fmt.Sprintf("otel: shutdown %s provider failed", e.signal)
}

func (e *providerShutdownFailure) Unwrap() error { return e.cause }

func providerShutdownError(signal string, err error) error {
	if err == nil {
		return nil
	}
	return &providerShutdownFailure{signal: signal, cause: err}
}
