package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStopAndConfirmStopsBeforeObservingTerminalState(t *testing.T) {
	stopped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/agents/a/sessions/s/stop":
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/agents/a/sessions/s":
			if !stopped {
				t.Fatal("terminal state was checked before stop")
			}
			_, _ = w.Write([]byte(`{"activity_status":"success"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stopAndConfirm(ctx, apiClient{baseURL: server.URL, http: server.Client()}, "a", "s"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteBindingRejectsMissingNonce(t *testing.T) {
	if _, err := writeBinding(t.TempDir(), "user", binding{Socket: "/tmp/bridge", Workdir: "/work"}); err == nil {
		t.Fatal("binding without nonce was accepted")
	}
}
