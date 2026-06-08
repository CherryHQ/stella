package observability

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func clearOTelEnv(t *testing.T) {
	for _, key := range []string{
		"OTEL_SDK_DISABLED",
		"OTEL_SERVICE_NAME",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
		"OTEL_TRACES_EXPORTER",
		"OTEL_LOGS_EXPORTER",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadConfigDisabled(t *testing.T) {
	clearOTelEnv(t)
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
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_SDK_DISABLED", "true")

	if LoadConfig().Enabled {
		t.Error("Enabled = true, want false when OTEL_SDK_DISABLED=true")
	}
}

func TestLoadConfigServiceName(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "custom-svc")

	if got := LoadConfig().ServiceName; got != "custom-svc" {
		t.Errorf("ServiceName = %q, want %q", got, "custom-svc")
	}
}

func TestLoadConfigEnabled(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")

	if !LoadConfig().Enabled {
		t.Errorf("Enabled = false, want true when endpoint set")
	}
}

func TestLoadConfigEnabledViaTracesExporter(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "console")

	if !LoadConfig().Enabled {
		t.Error("Enabled = false, want true when OTEL_TRACES_EXPORTER is set without an endpoint")
	}
}

func TestLoadConfigEnabledViaTracesEndpoint(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://localhost:4317")

	if !LoadConfig().Enabled {
		t.Error("Enabled = false, want true when traces-specific endpoint is set")
	}
}

func TestLoadConfigDisabledViaExporterNone(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")

	if LoadConfig().Enabled {
		t.Error("Enabled = true, want false when OTEL_TRACES_EXPORTER=none")
	}
}

func TestLoadConfigLogsEnabledViaLogsExporter(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_LOGS_EXPORTER", "console")

	cfg := LoadConfig()
	if cfg.TracesEnabled {
		t.Error("TracesEnabled = true, want false when only OTEL_LOGS_EXPORTER is set")
	}
	if !cfg.LogsEnabled {
		t.Error("LogsEnabled = false, want true when OTEL_LOGS_EXPORTER is set")
	}
}

func TestLoadConfigLogsDisabledViaExporterNone(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	cfg := LoadConfig()
	if !cfg.TracesEnabled {
		t.Error("TracesEnabled = false, want true when generic endpoint is set")
	}
	if cfg.LogsEnabled {
		t.Error("LogsEnabled = true, want false when OTEL_LOGS_EXPORTER=none")
	}
}

func TestInitDisabledIsNoOp(t *testing.T) {
	clearOTelEnv(t)
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
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

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

func TestInitLogsOnlyInstallsAndShutsDown(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_LOGS_EXPORTER", "console")

	beforeTracer := otel.GetTracerProvider()
	beforeSlog := slog.Default()
	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if p.tp != nil {
		t.Fatal("Init() installed a tracer provider when only logs are enabled")
	}
	if p.lp == nil {
		t.Fatal("Init() returned a no-op log provider while logs are enabled")
	}
	if otel.GetTracerProvider() != beforeTracer {
		t.Error("Init() replaced the global tracer provider when only logs are enabled")
	}
	if slog.Default() == beforeSlog {
		t.Error("Init() did not wrap the default slog logger")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown() error = %v, want nil", err)
	}
	if slog.Default() != beforeSlog {
		t.Error("Shutdown() did not restore the previous slog logger")
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
