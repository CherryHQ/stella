package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	logsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/CherryHQ/stella/internal/diagnostic"
)

func TestInitDiagnosticRedactsOTLPEndpointSecrets(t *testing.T) {
	clearOTelEnv(t)
	const raw = "https://user:canary-userinfo@collector.example:4318/v1/traces?api_key=canary-query#canary-fragment"
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", raw)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	previousSlog := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousSlog) })

	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})

	got := logs.String()
	for _, secret := range []string{"canary-userinfo", "canary-query", "canary-fragment"} {
		if strings.Contains(got, secret) {
			t.Fatalf("OTLP startup diagnostic leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://collector.example:4318/v1/traces") {
		t.Fatalf("OTLP startup diagnostic lost useful endpoint: %s", got)
	}
}

func TestExporterErrorsRedactOTLPEndpointSecrets(t *testing.T) {
	const raw = "https://user:canary-userinfo@collector.example:4318/v1/traces?api_key=canary-query#canary-fragment"
	cause := errors.New(raw)
	for name, err := range map[string]error{
		"initialization": exporterInitError("trace", diagnostic.Endpoint(raw), cause),
		"shutdown":       providerShutdownError("trace", cause),
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, cause) {
				t.Fatalf("errors.Is(%v, cause) = false, want true", err)
			}
			got := err.Error()
			for _, secret := range []string{"canary-userinfo", "canary-query", "canary-fragment"} {
				if strings.Contains(got, secret) {
					t.Fatalf("OTLP %s error leaked %q: %s", name, secret, got)
				}
			}
			if name == "initialization" && !strings.Contains(got, "https://collector.example:4318/v1/traces") {
				t.Fatalf("OTLP initialization error lost useful endpoint: %s", got)
			}
		})
	}
}

type controlledOTLPReceiver struct {
	tracev1.UnimplementedTraceServiceServer
	logsv1.UnimplementedLogsServiceServer

	traces   chan *tracev1.ExportTraceServiceRequest
	logCalls atomic.Int32
}

func (r *controlledOTLPReceiver) Export(_ context.Context, req *tracev1.ExportTraceServiceRequest) (*tracev1.ExportTraceServiceResponse, error) {
	select {
	case r.traces <- req:
		return &tracev1.ExportTraceServiceResponse{}, nil
	default:
		return nil, status.Error(codes.Internal, "unscripted trace export")
	}
}

func (r *controlledOTLPReceiver) ExportLogsService(_ context.Context, _ *logsv1.ExportLogsServiceRequest) (*logsv1.ExportLogsServiceResponse, error) {
	r.logCalls.Add(1)
	return nil, status.Error(codes.Unimplemented, "controlled receiver does not support logs")
}

func TestInitShutdownFlushesTraceAndDisablesUnsupportedLogs(t *testing.T) {
	clearOTelEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	receiver := &controlledOTLPReceiver{traces: make(chan *tracev1.ExportTraceServiceRequest, 1)}
	server := grpc.NewServer()
	tracev1.RegisterTraceServiceServer(server, receiver)
	logsv1.RegisterLogsServiceServer(server, logsServiceAdapter{receiver})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	beforeTracer := otel.GetTracerProvider()
	beforeSlog := slog.Default()
	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	shutdownDone := false
	shutdown := func() error {
		if shutdownDone {
			return nil
		}
		shutdownDone = true
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return p.Shutdown(ctx)
	}
	t.Cleanup(func() {
		_ = shutdown()
		slog.SetDefault(beforeSlog)
	})

	_, span := otel.Tracer("controlled-otlp-test").Start(context.Background(), "flush-on-shutdown")
	span.End()
	slog.Info("controlled unsupported logs")
	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
	defer flushCancel()
	if err := p.lp.ForceFlush(flushCtx); err != nil {
		t.Fatalf("log ForceFlush() error = %v", err)
	}
	if got := receiver.logCalls.Load(); got != 1 {
		t.Fatalf("unsupported logs calls = %d, want 1", got)
	}
	slog.Info("controlled log after disable")
	if err := p.lp.ForceFlush(flushCtx); err != nil {
		t.Fatalf("log ForceFlush() after disable error = %v", err)
	}
	if got := receiver.logCalls.Load(); got != 1 {
		t.Fatalf("unsupported logs calls after disable = %d, want 1", got)
	}

	if err := shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case req := <-receiver.traces:
		if !hasSpan(req, "flush-on-shutdown") {
			t.Fatal("shutdown did not flush the controlled trace")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trace was not flushed before shutdown deadline")
	}
	if otel.GetTracerProvider() != beforeTracer {
		t.Fatal("Shutdown() did not restore the tracer provider")
	}
	if slog.Default() != beforeSlog {
		t.Fatal("Shutdown() did not restore the default slog logger")
	}
}

func hasSpan(req *tracev1.ExportTraceServiceRequest, name string) bool {
	for _, resource := range req.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				if span.Name == name {
					return true
				}
			}
		}
	}
	return false
}

type logsServiceAdapter struct{ *controlledOTLPReceiver }

func (a logsServiceAdapter) Export(ctx context.Context, req *logsv1.ExportLogsServiceRequest) (*logsv1.ExportLogsServiceResponse, error) {
	return a.ExportLogsService(ctx, req)
}

type recordingLogsReceiver struct {
	logsv1.UnimplementedLogsServiceServer
	requests chan *logsv1.ExportLogsServiceRequest
}

func (r *recordingLogsReceiver) Export(_ context.Context, req *logsv1.ExportLogsServiceRequest) (*logsv1.ExportLogsServiceResponse, error) {
	r.requests <- req
	return &logsv1.ExportLogsServiceResponse{}, nil
}

type failingLogsReceiver struct {
	logsv1.UnimplementedLogsServiceServer
	calls atomic.Int32
}

func (r *failingLogsReceiver) Export(context.Context, *logsv1.ExportLogsServiceRequest) (*logsv1.ExportLogsServiceResponse, error) {
	r.calls.Add(1)
	return nil, status.Error(codes.Internal, "collector unavailable")
}

func TestInitThenSetupLoggerReachesOTLP(t *testing.T) {
	clearOTelEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &recordingLogsReceiver{requests: make(chan *logsv1.ExportLogsServiceRequest, 1)}
	server := grpc.NewServer()
	logsv1.RegisterLogsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	p, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()
	// This mirrors a logger captured by setup after Init has installed the tee.
	slog.With("hook", "trace").Info("setup logger reached exporter")
	flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.lp.ForceFlush(flushCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-receiver.requests:
		found := false
		for _, resource := range req.ResourceLogs {
			for _, scope := range resource.ScopeLogs {
				for _, record := range scope.LogRecords {
					if record.Body.GetStringValue() == "setup logger reached exporter" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatal("setup logger record missing from OTLP request")
		}
	case <-time.After(time.Second):
		t.Fatal("setup logger record did not reach OTLP")
	}
}

func TestOTLPLogLegHonorsConfiguredLevel(t *testing.T) {
	clearOTelEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &recordingLogsReceiver{requests: make(chan *logsv1.ExportLogsServiceRequest, 20)}
	server := grpc.NewServer()
	logsv1.RegisterLogsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	defer func() { server.Stop(); _ = listener.Close() }()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	defer slog.SetDefault(previous)
	for _, level := range []struct {
		value string
		want  bool
	}{{"INFO", false}, {"DEBUG", true}} {
		t.Run(level.value, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
			t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
			t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
			t.Setenv("OTEL_TRACES_EXPORTER", "none")
			t.Setenv("OTEL_METRICS_EXPORTER", "none")
			t.Setenv("LOG_LEVEL", level.value)
			p, err := Init(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			slog.Debug("level floor probe")
			flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := p.ForceFlushLogs(flushCtx); err != nil {
				t.Fatal(err)
			}
			cancel()
			found := false
			for {
				select {
				case req := <-receiver.requests:
					for _, resource := range req.ResourceLogs {
						for _, scope := range resource.ScopeLogs {
							for _, record := range scope.LogRecords {
								if record.Body.GetStringValue() == "level floor probe" {
									found = true
								}
							}
						}
					}
				default:
					if found != level.want {
						t.Fatalf("debug exported=%v at LOG_LEVEL=%s", found, level.value)
					}
					_ = p.Shutdown(context.Background())
					return
				}
			}
		})
	}
}

func TestOTelExporterErrorsUseConsoleOnlyHandler(t *testing.T) {
	clearOTelEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &failingLogsReceiver{}
	server := grpc.NewServer()
	logsv1.RegisterLogsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	var console bytes.Buffer
	previousSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&console, nil)))
	t.Cleanup(func() { slog.SetDefault(previousSlog) })
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	p, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()
	slog.Info("one exported record")
	flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = p.lp.ForceFlush(flushCtx)
	if receiver.calls.Load() == 0 {
		t.Fatal("log exporter was not called")
	}
	if strings.Count(console.String(), "otel SDK error") == 0 || !strings.Contains(console.String(), "component=otel") {
		t.Fatalf("missing console-only OTel error: %s", console.String())
	}
	before := receiver.calls.Load()
	_ = p.lp.ForceFlush(flushCtx)
	if receiver.calls.Load() != before {
		t.Fatalf("error handler caused another OTLP export: before=%d after=%d", before, receiver.calls.Load())
	}
}
