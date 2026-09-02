package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerPassesThrough(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") // tracing disabled: wrapper is a no-op

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("X-Inner", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	})

	rr := httptest.NewRecorder()
	Handler(inner).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))

	if !called {
		t.Fatal("wrapped handler did not invoke the inner handler")
	}
	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusTeapot)
	}
	if got := rr.Header().Get("X-Inner"); got != "yes" {
		t.Errorf("inner header not propagated: X-Inner = %q", got)
	}
	if rr.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "ok")
	}
}
