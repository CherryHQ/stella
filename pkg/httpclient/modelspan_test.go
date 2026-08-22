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

func newTestClient(handler http.HandlerFunc) (*httptest.Server, *http.Client) {
	srv := httptest.NewServer(handler)
	return srv, &http.Client{Transport: modelSpanTransport{base: http.DefaultTransport}}
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
	srv, client := newTestClient(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	defer srv.Close()

	// One logical model request, two provider HTTP requests: what an SDK-level
	// retry looks like from here.
	ctx, req := ai.WithModelRequest(context.Background(), "claude-3")
	get(t, ctx, client, srv.URL+"/v1/messages?key=sk-secret")
	get(t, ctx, client, srv.URL+"/v1/messages?key=sk-secret")

	stubs := tracetest.SpanStubsFromReadOnlySpans(sr.Ended())
	var attempts []int64
	for _, s := range stubs {
		if s.Name != "gen_ai.chat.request" {
			continue
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
		// The query string can carry the API key on some gateways, so only the
		// host may appear on an exported span.
		for _, kv := range s.Attributes {
			if strings.Contains(kv.Value.Emit(), "sk-secret") {
				t.Fatalf("attribute %s leaked the request query: %s", kv.Key, kv.Value.Emit())
			}
		}
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("attempt numbers = %v, want [1 2]", attempts)
	}
	if req.Attempts() != 2 {
		t.Errorf("req.Attempts() = %d, want 2", req.Attempts())
	}
}

func TestModelSpanSkippedOutsideAModelRequest(t *testing.T) {
	sr := recordingProvider(t)
	srv, client := newTestClient(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	defer srv.Close()

	get(t, context.Background(), client, srv.URL)

	for _, s := range tracetest.SpanStubsFromReadOnlySpans(sr.Ended()) {
		if s.Name == "gen_ai.chat.request" {
			t.Fatal("a plain HTTP call produced a model-request span")
		}
	}
}

func TestModelSpanMarksHTTPFailure(t *testing.T) {
	sr := recordingProvider(t)
	srv, client := newTestClient(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	defer srv.Close()

	ctx, _ := ai.WithModelRequest(context.Background(), "claude-3")
	get(t, ctx, client, srv.URL)

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
}
