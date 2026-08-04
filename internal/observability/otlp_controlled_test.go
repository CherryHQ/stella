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

	"github.com/CherryHQ/stella/pkg/endpoint"
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
		"initialization": exporterInitError("trace", endpoint.ForDiagnostic(raw), cause),
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
