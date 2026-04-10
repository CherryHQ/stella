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
	if !otelEnabled() {
		t.Error("expected otelEnabled=true when env is set")
	}
}

func TestTransport_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tr := transport()
	if tr != http.DefaultTransport {
		t.Error("expected DefaultTransport when OTel is disabled")
	}
}

func TestTransport_WithOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	tr := transport()
	if tr == http.DefaultTransport {
		t.Error("expected wrapped transport when OTel is enabled")
	}
}
