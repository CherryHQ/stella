package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAccessLogServer(buf *bytes.Buffer) *Server {
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return &Server{log: slog.New(handler)}
}

func TestAccessLogMiddlewareLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	s := newAccessLogServer(&buf)

	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents", nil))

	out := buf.String()
	for _, want := range []string{"level=INFO", `msg="http request"`, "method=POST", "path=/api/agents", "status=201", "bytes=5"} {
		if !strings.Contains(out, want) {
			t.Errorf("access log missing %q: %q", want, out)
		}
	}
}

func TestAccessLogMiddlewareLevels(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		status    int
		wantLevel string
	}{
		{"server error is ERROR", "/api/agents", http.StatusInternalServerError, "level=ERROR"},
		{"client error is INFO", "/api/agents", http.StatusUnauthorized, "level=INFO"},
		{"health probe is DEBUG", "/healthz", http.StatusOK, "level=DEBUG"},
		{"static asset is DEBUG", "/assets/app.js", http.StatusOK, "level=DEBUG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := newAccessLogServer(&buf)
			h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if out := buf.String(); !strings.Contains(out, tc.wantLevel) {
				t.Errorf("want %s in access log, got: %q", tc.wantLevel, out)
			}
		})
	}
}

// TestAccessLogMiddlewarePreservesFlusher guards SSE streaming: session and
// group chat handlers assert w.(http.Flusher) and refuse to stream without it.
func TestAccessLogMiddlewarePreservesFlusher(t *testing.T) {
	var buf bytes.Buffer
	s := newAccessLogServer(&buf)

	flushed := false
	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer lost http.Flusher")
		}
		_, _ = w.Write([]byte("data: hi\n\n"))
		f.Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/1/events", nil))
	flushed = rec.Flushed

	if !flushed {
		t.Error("Flush was not forwarded to the underlying writer")
	}
	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("default status should be 200: %q", buf.String())
	}
}
