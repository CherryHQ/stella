package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialRequestAccessLogNeverIncludesWriteOnlyKey(t *testing.T) {
	const secret = "agent-provider-write-only-secret"
	var logs bytes.Buffer
	s := newAccessLogServer(&logs)
	handler := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPatch, "/api/agents/a/provider-credentials/p", strings.NewReader(`{"api_key":"`+secret+`"}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if got := logs.String(); strings.Contains(got, secret) {
		t.Fatalf("access log leaked write-only key: %q", got)
	}
}
