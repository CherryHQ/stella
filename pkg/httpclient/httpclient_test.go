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

// clearOTelEnv unsets what the tracing predicate reads so each case states its
// own world.
func clearOTelEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_SDK_DISABLED",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_TRACES_EXPORTER",
	} {
		t.Setenv(k, "")
	}
}

// The model-span wrapper is always outermost (it counts provider retries with
// or without export), so these assert what it wraps.
func TestTransport_NoOtel(t *testing.T) {
	clearOTelEnv(t)
	tr, ok := transport().(modelSpanTransport)
	if !ok {
		t.Fatalf("expected a modelSpanTransport, got %T", transport())
	}
	if tr.next != http.DefaultTransport || tr.plain != http.DefaultTransport {
		t.Error("expected DefaultTransport under the model-span transport when OTel is disabled")
	}
	if tr.tracing {
		t.Error("expected tracing off when OTel is disabled")
	}
}

func TestTransport_WithOtel(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	tr, ok := transport().(modelSpanTransport)
	if !ok {
		t.Fatalf("expected a modelSpanTransport, got %T", transport())
	}
	if tr.next == http.DefaultTransport {
		t.Error("expected an otelhttp-wrapped transport for non-model requests when OTel is enabled")
	}
	// Model requests must not go through otelhttp: it exports url.full and its
	// client span outlives the response headers.
	if tr.plain != http.DefaultTransport {
		t.Error("expected model requests to bypass otelhttp")
	}
	if !tr.tracing {
		t.Error("expected tracing on when OTel is enabled")
	}
}

// The transport and the tracer provider must agree in both directions. These
// are the cases where a second, hand-rolled predicate got it wrong: a
// traces-specific endpoint left the transport silent under a live provider,
// and OTEL_TRACES_EXPORTER=none left it spanning after export was switched off.
func TestTransport_FollowsTheTracingPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "traces-specific endpoint",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://localhost:4318/v1/traces"},
			want: true,
		},
		{
			name: "exporter none",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
				"OTEL_TRACES_EXPORTER":        "none",
			},
			want: false,
		},
		{
			name: "sdk disabled",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
				"OTEL_SDK_DISABLED":           "true",
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearOTelEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			tr, ok := transport().(modelSpanTransport)
			if !ok {
				t.Fatalf("expected a modelSpanTransport, got %T", transport())
			}
			if tr.tracing != tc.want {
				t.Errorf("tracing = %v, want %v", tr.tracing, tc.want)
			}
			if otelWrapped := tr.next != http.DefaultTransport; otelWrapped != tc.want {
				t.Errorf("otelhttp wrapping = %v, want %v", otelWrapped, tc.want)
			}
		})
	}
}
