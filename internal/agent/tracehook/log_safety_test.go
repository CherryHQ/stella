package tracehook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	v1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"

	"github.com/CherryHQ/stella/internal/platform/observability"
	"github.com/CherryHQ/stella/pkg/hooks"
)

type logCaptureServer struct {
	v1.UnimplementedLogsServiceServer
	requests chan *v1.ExportLogsServiceRequest
}

func (s *logCaptureServer) Export(_ context.Context, req *v1.ExportLogsServiceRequest) (*v1.ExportLogsServiceResponse, error) {
	s.requests <- req
	return &v1.ExportLogsServiceResponse{}, nil
}

func TestHookErrorTextStaysOffOTLPLogLeg(t *testing.T) {
	for _, key := range []string{
		"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_INSECURE", "OTEL_TRACES_EXPORTER", "OTEL_LOGS_EXPORTER", "OTEL_METRICS_EXPORTER",
	} {
		t.Setenv(key, "")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &logCaptureServer{requests: make(chan *v1.ExportLogsServiceRequest, 1)}
	server := grpc.NewServer()
	v1.RegisterLogsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&discardWriter{}, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	provider, err := observability.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()
	hook := New(false, false)
	secret := "provider-body-secret-should-not-leave"
	hook.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		Model: "model", Error: errors.New(secret), Duration: time.Millisecond,
	})
	flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := provider.ForceFlushLogs(flushCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-receiver.requests:
		wire := fmt.Sprint(req)
		if wire == "" {
			t.Fatal("empty OTLP log request")
		}
		if strings.Contains(wire, secret) {
			t.Fatalf("OTLP log contains raw error text: %s", wire)
		}
	case <-time.After(time.Second):
		t.Fatal("hook log did not reach OTLP")
	}
}

type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }
