package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

// initForTest runs the real Init with a console exporter, so these tests
// exercise the production initialization path. Faking the precondition — an
// explicit otel.SetTextMapPropagator in the test — is what let a no-op
// propagator ship: the test passed and production propagated nothing.
func initForTest(t *testing.T) {
	t.Helper()
	clearOTelEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
}

// resetPropagatorToProcessDefault restores the state a fresh process starts in:
// the SDK's default propagator, which is a no-op. Sibling tests in this package
// install and restore providers, so the global can carry over between them;
// this recreates production's starting point rather than inventing one.
func resetPropagatorToProcessDefault(t *testing.T) {
	t.Helper()
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
}

func TestInitInstallsPropagation(t *testing.T) {
	resetPropagatorToProcessDefault(t)
	if fields := otel.GetTextMapPropagator().Fields(); len(fields) != 0 {
		t.Fatalf("the SDK default propagator carries %v, want the no-op default", fields)
	}

	initForTest(t)

	fields := strings.Join(otel.GetTextMapPropagator().Fields(), ",")
	for _, want := range []string{"traceparent", "baggage"} {
		if !strings.Contains(fields, want) {
			t.Errorf("propagator fields = %q, want %q among them", fields, want)
		}
	}
}

func TestShutdownRestoresThePreviousPropagator(t *testing.T) {
	resetPropagatorToProcessDefault(t)
	clearOTelEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	before := otel.GetTextMapPropagator()
	p, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got := otel.GetTextMapPropagator().Fields(); len(got) != len(before.Fields()) {
		t.Errorf("propagator after shutdown = %v, want the previous one %v", got, before.Fields())
	}
}

// The shared HTTP client must carry the current span and baggage to the wire.
// This is the pairing that matters: real Init, real client, real request.
func TestOutboundRequestCarriesTraceContext(t *testing.T) {
	initForTest(t)

	var gotTraceparent, gotBaggage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		gotBaggage = r.Header.Get("baggage")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	member, err := baggage.NewMember("tenant", "acme")
	if err != nil {
		t.Fatal(err)
	}
	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}
	ctx := baggage.ContextWithBaggage(context.Background(), bag)
	ctx, span := otel.Tracer("test").Start(ctx, "caller")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpclient.StdHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if gotTraceparent == "" {
		t.Fatal("no traceparent reached the server")
	}
	// 00-<trace id>-<span id>-<flags>: the trace must be the caller's, and the
	// sampling flag must survive, or the downstream side drops the spans.
	parts := strings.Split(gotTraceparent, "-")
	if len(parts) != 4 {
		t.Fatalf("malformed traceparent %q", gotTraceparent)
	}
	if parts[1] != span.SpanContext().TraceID().String() {
		t.Errorf("traceparent trace id = %s, want %s", parts[1], span.SpanContext().TraceID())
	}
	if parts[3] != "01" {
		t.Errorf("traceparent flags = %s, want 01 (sampled)", parts[3])
	}
	if !strings.Contains(gotBaggage, "tenant=acme") {
		t.Errorf("baggage header = %q, want tenant=acme", gotBaggage)
	}
}

// The inbound side of the same wire format: a request arriving with a
// traceparent must continue that trace, not start a new one.
func TestInboundRequestExtractsTraceContext(t *testing.T) {
	initForTest(t)

	var serverCtx trace.SpanContext
	srv := httptest.NewServer(Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCtx = trace.SpanContextFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	ctx, span := otel.Tracer("test").Start(context.Background(), "caller")
	defer span.End()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpclient.StdHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if serverCtx.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("server trace id = %s, want the caller's %s",
			serverCtx.TraceID(), span.SpanContext().TraceID())
	}
}
