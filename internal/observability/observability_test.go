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
}

func TestLoadConfigDisabledViaKillSwitch(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_SDK_DISABLED", "true")

	if LoadConfig().Enabled {
		t.Error("Enabled = true, want false when OTEL_SDK_DISABLED=true")
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

func TestTemplatePath(t *testing.T) {
	cases := map[string]string{
		"/api/projects/123":                                "/api/projects/:id",
		"/api/projects/123/tasks/456":                      "/api/projects/:id/tasks/:id",
		"/api/agents/9f8b7c6d-1234-5678-9abc-def012345678": "/api/agents/:id",
		"/api/users/me":                                    "/api/users/me",
		"/health":                                          "/health",
		"":                                                 "/",
		"/api/sessions/01HZX9K3M7Q2N5P8R1T4V6W8YB": "/api/sessions/:id",
	}
	for in, want := range cases {
		if got := templatePath(in); got != want {
			t.Errorf("templatePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShutdownNilProvider(t *testing.T) {
	var p *Provider
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Provider Shutdown() = %v, want nil", err)
	}
}
