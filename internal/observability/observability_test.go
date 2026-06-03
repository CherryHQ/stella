package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestLoadConfigDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "")

	cfg := LoadConfig()
	if cfg.Enabled {
		t.Errorf("Enabled = true, want false when endpoint unset")
	}
	if cfg.ServiceName != "stella" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "stella")
	}
	if cfg.SampleRate != 1.0 {
		t.Errorf("SampleRate = %v, want 1.0", cfg.SampleRate)
	}
}

func TestLoadConfigServiceName(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "custom-svc")

	if got := LoadConfig().ServiceName; got != "custom-svc" {
		t.Errorf("ServiceName = %q, want %q", got, "custom-svc")
	}
}

func TestLoadConfigEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")

	if !LoadConfig().Enabled {
		t.Errorf("Enabled = false, want true when endpoint set")
	}
}

func TestInitDisabledIsNoOp(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	before := otel.GetTracerProvider()
	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil Provider")
	}
	if otel.GetTracerProvider() != before {
		t.Error("Init() replaced the global tracer provider while disabled")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() error = %v, want nil", err)
	}
}

func TestInitEnabledInstallsAndShutsDown(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if p.tp == nil {
		t.Fatal("Init() returned a no-op Provider while enabled")
	}
	if otel.GetTracerProvider() != p.tp {
		t.Error("Init() did not install the provider as global")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown() error = %v, want nil", err)
	}
}

func TestShutdownNilProvider(t *testing.T) {
	var p *Provider
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Provider Shutdown() = %v, want nil", err)
	}
}
