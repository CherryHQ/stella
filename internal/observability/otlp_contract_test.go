package observability

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type otlpTraceCollector struct {
	collectortracev1.UnimplementedTraceServiceServer

	mu       sync.Mutex
	attempts int
	requests []*collectortracev1.ExportTraceServiceRequest
}

// Export intentionally fails the first request with a retryable status. The
// exporter must recover and resend the same completed span before shutdown.
func (c *otlpTraceCollector) Export(
	_ context.Context,
	req *collectortracev1.ExportTraceServiceRequest,
) (*collectortracev1.ExportTraceServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	if c.attempts == 1 {
		return nil, status.Error(codes.Unavailable, "temporary collector outage")
	}
	cloned, ok := proto.Clone(req).(*collectortracev1.ExportTraceServiceRequest)
	if !ok {
		return nil, status.Error(codes.Internal, "clone trace request")
	}
	c.requests = append(c.requests, cloned)
	return &collectortracev1.ExportTraceServiceResponse{}, nil
}

func (c *otlpTraceCollector) snapshot() (int, []*collectortracev1.ExportTraceServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]*collectortracev1.ExportTraceServiceRequest, len(c.requests))
	copy(requests, c.requests)
	return c.attempts, requests
}

// TestOTLPTraceExportContract runs a real local OTLP/gRPC receiver and proves
// retry, Run-ID correlation, payload queryability, shutdown flush, and global
// provider restoration without relying on an external telemetry service.
func TestOTLPTraceExportContract(t *testing.T) {
	clearOTelEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for OTLP collector: %v", err)
	}
	grpcServer := grpc.NewServer()
	collector := &otlpTraceCollector{}
	collectortracev1.RegisterTraceServiceServer(grpcServer, collector)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		if serveErr := <-serveDone; serveErr != nil {
			t.Errorf("serve OTLP collector: %v", serveErr)
		}
	})

	const (
		runID        = "release-contract-run"
		serviceName  = "stella-release-contract"
		sentinel     = "otlp-contract-secret-canary"
		spanName     = "release.contract"
		resourceAttr = "release.run_id"
	)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "15000")
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_SERVICE_NAME", serviceName)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", resourceAttr+"="+runID)
	t.Setenv("STELLA_RELEASE_CONTRACT_SECRET", sentinel)

	previous := otel.GetTracerProvider()
	provider, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if provider.tp == nil {
		t.Fatal("Init returned a no-op trace provider")
	}

	_, span := otel.Tracer("stella-release-contract").Start(context.Background(), spanName)
	span.SetAttributes(attribute.String(resourceAttr, runID))
	span.End()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	shutdownErr := provider.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		t.Fatalf("Shutdown: %v", shutdownErr)
	}
	if otel.GetTracerProvider() != previous {
		t.Fatal("Shutdown did not restore the previous global tracer provider")
	}

	attempts, requests := collector.snapshot()
	if attempts < 2 {
		t.Fatalf("OTLP export attempts = %d, want a retry after Unavailable", attempts)
	}
	if len(requests) == 0 {
		t.Fatal("collector received no successful OTLP export")
	}
	if !containsOTLPSpan(requests, serviceName, runID, spanName) {
		t.Fatalf(
			"collector did not receive service=%q run_id=%q span=%q",
			serviceName,
			runID,
			spanName,
		)
	}
	for _, req := range requests {
		raw, marshalErr := proto.Marshal(req)
		if marshalErr != nil {
			t.Fatalf("marshal captured OTLP request: %v", marshalErr)
		}
		if strings.Contains(string(raw), sentinel) {
			t.Fatal("captured OTLP payload contains the release secret canary")
		}
	}
}

func containsOTLPSpan(
	requests []*collectortracev1.ExportTraceServiceRequest,
	serviceName string,
	runID string,
	spanName string,
) bool {
	for _, request := range requests {
		for _, resourceSpans := range request.ResourceSpans {
			if resourceAttribute(resourceSpans.Resource, "service.name") != serviceName ||
				resourceAttribute(resourceSpans.Resource, "release.run_id") != runID {
				continue
			}
			for _, scopeSpans := range resourceSpans.ScopeSpans {
				for _, span := range scopeSpans.Spans {
					if span.Name == spanName && keyValue(span.Attributes, "release.run_id") == runID {
						return true
					}
				}
			}
		}
	}
	return false
}

func resourceAttribute(resource *resourcev1.Resource, key string) string {
	if resource == nil {
		return ""
	}
	return keyValue(resource.Attributes, key)
}

func keyValue(attributes []*commonv1.KeyValue, key string) string {
	for _, item := range attributes {
		if item.Key == key && item.Value != nil {
			return item.Value.GetStringValue()
		}
	}
	return ""
}
