package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPIgnoresForwardedHeadersByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.20")
	req.Header.Set("X-Real-IP", "198.51.100.30")

	if got := clientIP(req); got != "203.0.113.10" {
		t.Fatalf("clientIP = %q, want remote addr", got)
	}
}

func TestClientIPUsesForwardedHeadersFromTrustedProxy(t *testing.T) {
	t.Setenv("STELLA_TRUSTED_PROXIES", "203.0.113.10")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.20, 203.0.113.10")

	if got := clientIP(req); got != "198.51.100.20" {
		t.Fatalf("clientIP = %q, want forwarded addr", got)
	}
}

func TestClientIPFallsBackWhenTrustedProxySendsInvalidHeader(t *testing.T) {
	t.Setenv("STELLA_TRUSTED_PROXIES", "203.0.113.0/24")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	if got := clientIP(req); got != "203.0.113.10" {
		t.Fatalf("clientIP = %q, want remote addr", got)
	}
}
