package mcp

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestNewHTTPClientAppliesConfiguredHeaders(t *testing.T) {
	client := newHTTPClient(15, map[string]string{
		"Authorization": "Bearer token",
		"X-Test":        "value",
	})
	if client.Timeout.Seconds() != 15 {
		t.Fatalf("timeout = %v, want 15s", client.Timeout)
	}

	rt, ok := client.Transport.(headerRoundTripper)
	if !ok {
		t.Fatalf("client.Transport = %T, want headerRoundTripper", client.Transport)
	}
	rt.base = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer token")
		}
		if got := req.Header.Get("X-Test"); got != "value" {
			t.Fatalf("X-Test = %q, want %q", got, "value")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})

	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = res.Body.Close()
}

func TestFlattenEnvIsDeterministic(t *testing.T) {
	got := flattenEnv(map[string]string{"B": "2", "A": "1"})
	want := []string{"A=1", "B=2"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
