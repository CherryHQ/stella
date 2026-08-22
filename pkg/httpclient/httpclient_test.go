package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
}

func TestNewWithTimeout(t *testing.T) {
	c := NewWithTimeout(5 * time.Second)
	if c == nil {
		t.Fatal("NewWithTimeout() returned nil")
	}
}

func TestStdHTTPClient(t *testing.T) {
	c := StdHTTPClient()
	if c == nil {
		t.Fatal("StdHTTPClient() returned nil")
	} else {
		if c.Transport == nil {
			t.Error("expected non-nil transport")
		}
		// No timeout set on StdHTTPClient (SSE/streaming use context deadline).
		if c.Timeout != 0 {
			t.Errorf("expected zero timeout, got %v", c.Timeout)
		}
	}
}

func TestOtelEnabled_DefaultFalse(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if otelEnabled() {
		t.Error("expected otelEnabled=false when env is unset")
	}
}

func TestOtelEnabled_True(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_SDK_DISABLED", "")
	if !otelEnabled() {
		t.Error("expected otelEnabled=true when env is set")
	}
}

func TestOtelEnabled_DisabledByKillSwitch(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if otelEnabled() {
		t.Error("expected otelEnabled=false when OTEL_SDK_DISABLED=true")
	}
}

// The model-span wrapper is always outermost (it counts provider retries with
// or without export), so these assert what it wraps.
func TestTransport_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tr, ok := transport().(modelSpanTransport)
	if !ok {
		t.Fatalf("expected a modelSpanTransport, got %T", transport())
	}
	if tr.base != http.DefaultTransport {
		t.Error("expected DefaultTransport under the model-span transport when OTel is disabled")
	}
}

func TestTransport_WithOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_SDK_DISABLED", "")
	tr, ok := transport().(modelSpanTransport)
	if !ok {
		t.Fatalf("expected a modelSpanTransport, got %T", transport())
	}
	if tr.base == http.DefaultTransport {
		t.Error("expected an otelhttp-wrapped base when OTel is enabled")
	}
}
