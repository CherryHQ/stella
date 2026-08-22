package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/CherryHQ/stella/pkg/ai"
)

// The secret a provider gateway can hide in a URL. Every span assertion below
// checks the whole recorded span set for it, not just the span this package
// creates: an instrumentation layer underneath us exports url.full too.
const (
	urlSecret = "sk-abcdef0123456789xyz"
	urlPath   = "/proxy/v1/messages"
)

// recordingProvider installs an in-memory tracer provider and restores the
// previous one, so tests never leak a recording provider into each other.
func recordingProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return sr
}

func attrValueOK(stub tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range stub.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func spanAttr(t *testing.T, stub tracetest.SpanStub, key string) attribute.Value {
	t.Helper()
	for _, kv := range stub.Attributes {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	t.Fatalf("span %q has no attribute %q (has %v)", stub.Name, key, stub.Attributes)
	return attribute.Value{}
}

// assertNoURLLeak fails if any attribute or event of any recorded span carries
// the request path or its query secret.
func assertNoURLLeak(t *testing.T, stubs tracetest.SpanStubs) {
	t.Helper()
	check := func(span, key, value string) {
		if strings.Contains(value, urlSecret) || strings.Contains(value, urlPath) {
			t.Errorf("span %s attribute %s leaked the request URL: %s", span, key, value)
		}
	}
	for _, s := range stubs {
		for _, kv := range s.Attributes {
			check(s.Name, string(kv.Key), kv.Value.Emit())
		}
		for _, ev := range s.Events {
			for _, kv := range ev.Attributes {
				check(s.Name, string(kv.Key), kv.Value.Emit())
			}
		}
		check(s.Name, "status", s.Status.Description)
	}
}

// tracingTransport builds the shared transport with tracing on, so tests
// exercise exactly what production installs.
func tracingTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	return transport()
}

func newTestServer(t *testing.T, status int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, ctx context.Context, client *http.Client, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestModelSpanPerAttempt(t *testing.T) {
	sr := recordingProvider(t)
	srv := newTestServer(t, http.StatusOK)
	client := &http.Client{Transport: tracingTransport(t)}

	// One logical model request, two provider HTTP requests: what an SDK-level
	// retry looks like from here.
	ctx, req := ai.WithModelRequest(context.Background(), "claude-3")
	target := srv.URL + urlPath + "?key=" + urlSecret
	get(t, ctx, client, target)
	get(t, ctx, client, target)

	stubs := tracetest.SpanStubsFromReadOnlySpans(sr.Ended())
	// Exactly one span per attempt, with one meaning. otelhttp would add a
	// second client span per request that outlives this one.
	if len(stubs) != 2 {
		names := make([]string, len(stubs))
		for i, s := range stubs {
			names[i] = s.Name
		}
		t.Fatalf("two attempts produced %d spans (%v), want 2", len(stubs), names)
	}
	var attempts []int64
	for _, s := range stubs {
		if s.Name != "gen_ai.chat.request" {
			t.Fatalf("unexpected span %q", s.Name)
		}
		if got := spanAttr(t, s, "gen_ai.request.model").AsString(); got != "claude-3" {
			t.Errorf("gen_ai.request.model = %q, want claude-3", got)
		}
		if got := spanAttr(t, s, "gen_ai.operation.name").AsString(); got != "chat" {
			t.Errorf("gen_ai.operation.name = %q, want chat", got)
		}
		if got := spanAttr(t, s, "http.response.status_code").AsInt64(); got != 200 {
			t.Errorf("status code = %d, want 200", got)
		}
		attempts = append(attempts, spanAttr(t, s, "gen_ai.request.attempt").AsInt64())
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("attempt numbers = %v, want [1 2]", attempts)
	}
	if req.Attempts() != 2 {
		t.Errorf("req.Attempts() = %d, want 2", req.Attempts())
	}
	assertNoURLLeak(t, stubs)
}

// The safety invariant belongs to the transport, not to the caller: a request
// that never heard of ai.ModelRequest — a channel, a webhook, a classifier
// calling a provider directly — still records the host and nothing more. An
// opt-in marker would make credential safety depend on every call site.
func TestClientSpanNeverRecordsTheURLWithoutAMarker(t *testing.T) {
	sr := recordingProvider(t)
	srv := newTestServer(t, http.StatusOK)
	client := &http.Client{Transport: tracingTransport(t)}

	get(t, context.Background(), client, srv.URL+urlPath+"?key="+urlSecret)

	stubs := tracetest.SpanStubsFromReadOnlySpans(sr.Ended())
	if len(stubs) != 1 {
		t.Fatalf("got %d spans, want 1", len(stubs))
	}
	if stubs[0].Name != "HTTP GET" {
		t.Errorf("span name = %q, want the generic client name", stubs[0].Name)
	}
	if _, ok := attrValueOK(stubs[0], "gen_ai.request.attempt"); ok {
		t.Error("a plain HTTP call produced gen_ai attributes")
	}
	assertNoURLLeak(t, stubs)
}

// A transport error is a *url.Error carrying the request URL; nothing derived
// from its message may reach the span, marker or not.
func TestClientSpanNeverRecordsTransportErrorText(t *testing.T) {
	sr := recordingProvider(t)
	// Port 0 is unroutable, so the RoundTrip fails inside the transport.
	client := &http.Client{Transport: tracingTransport(t)}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://127.0.0.1:0"+urlPath+"?key="+urlSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil { //nolint:bodyclose // the request must fail
		t.Fatal("expected the request to fail")
	}

	stubs := tracetest.SpanStubsFromReadOnlySpans(sr.Ended())
	if len(stubs) != 1 {
		t.Fatalf("got %d spans, want 1", len(stubs))
	}
	if stubs[0].Status.Code != codes.Error {
		t.Errorf("status = %v, want Error", stubs[0].Status.Code)
	}
	if stubs[0].Status.Description != "http request failed" {
		t.Errorf("status description = %q, want the fixed text", stubs[0].Status.Description)
	}
	assertNoURLLeak(t, stubs)
}

// Propagation is asserted where it actually happens — internal/observability,
// through the real Init — because that is where the global propagator is
// installed. Setting one up here would prove only that the test set it up.
// What belongs to this layer: the caller's request is never the object handed
// downstream, so header injection cannot leak back into it.
func TestClientSpanClonesBeforeSendingDownstream(t *testing.T) {
	recordingProvider(t)
	capture := &capturingRoundTripper{}
	tr := clientSpanTransport{base: capture, tracing: true}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://gw.example"+urlPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Caller", "kept")

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if capture.req == req {
		t.Fatal("the caller's own request was handed downstream")
	}
	if capture.req.Header.Get("X-Caller") != "kept" {
		t.Error("the clone lost a caller header")
	}
	capture.req.Header.Set("X-Injected", "1")
	if req.Header.Get("X-Injected") != "" {
		t.Error("the clone shares the caller's header map")
	}
}

type capturingRoundTripper struct{ req *http.Request }

func (c *capturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestModelSpanMarksHTTPFailure(t *testing.T) {
	sr := recordingProvider(t)
	srv := newTestServer(t, http.StatusTooManyRequests)
	client := &http.Client{Transport: tracingTransport(t)}

	ctx, _ := ai.WithModelRequest(context.Background(), "claude-3")
	get(t, ctx, client, srv.URL+urlPath+"?key="+urlSecret)

	stubs := tracetest.SpanStubsFromReadOnlySpans(sr.Ended())
	if len(stubs) != 1 {
		t.Fatalf("got %d spans, want 1", len(stubs))
	}
	if stubs[0].Status.Code != codes.Error {
		t.Errorf("status = %v, want Error", stubs[0].Status.Code)
	}
	if got := spanAttr(t, stubs[0], "http.response.status_code").AsInt64(); got != 429 {
		t.Errorf("status code = %d, want 429", got)
	}
	assertNoURLLeak(t, stubs)
}

// OTEL_SDK_DISABLED (and an unconfigured exporter) must silence these spans
// even when something else in the process installed a real tracer provider.
func TestModelSpanRespectsTheKillSwitch(t *testing.T) {
	sr := recordingProvider(t)
	srv := newTestServer(t, http.StatusOK)
	client := &http.Client{Transport: clientSpanTransport{base: http.DefaultTransport, tracing: false}}

	ctx, req := ai.WithModelRequest(context.Background(), "claude-3")
	get(t, ctx, client, srv.URL)

	if n := len(sr.Ended()); n != 0 {
		t.Errorf("tracing disabled still produced %d spans", n)
	}
	// Counting is not tracing: the attempt count feeds logs and the hook
	// payload, so it must survive the kill switch.
	if req.Attempts() != 1 {
		t.Errorf("Attempts() = %d, want 1", req.Attempts())
	}
}

// stubRoundTripper answers without touching the network so the allocation
// count below measures only this package's work.
type stubRoundTripper struct{}

func (stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// With tracing off the transport must be free: no attribute slice, no span, no
// request clone. This is a hot path — one model request per turn, more with
// retries — and it is what "cheap no-op when OTel is disabled" has to mean.
func TestModelSpanDoesNotAllocateWhenTracingIsOff(t *testing.T) {
	recordingProvider(t) // a live provider must not tempt it into spanning
	stub := stubRoundTripper{}
	tr := clientSpanTransport{base: stub, tracing: false}

	ctx, _ := ai.WithModelRequest(context.Background(), "claude-3")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://gw.example"+urlPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	roundTrip := func(rt http.RoundTripper) float64 {
		return testing.AllocsPerRun(200, func() {
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		})
	}
	// The stub itself allocates its response; the wrapper must add nothing.
	baseline := roundTrip(stub)
	if got := roundTrip(tr); got > baseline {
		t.Errorf("RoundTrip allocated %.1f times with tracing off, want %.1f (the bare transport)", got, baseline)
	}
}
