package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestOriginUsesOriginHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/auth/profile/oauth/feishu/start", nil)
	req.Header.Set("Origin", "http://localhost:25678")

	if got := requestOrigin(req); got != "http://localhost:25678" {
		t.Fatalf("requestOrigin = %q, want http://localhost:25678", got)
	}
}

func TestRequestOriginFallsBackToRequestHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://localhost:25678/api/auth/profile/oauth/feishu/callback", nil)

	if got := requestOrigin(req); got != "http://localhost:25678" {
		t.Fatalf("requestOrigin = %q, want http://localhost:25678", got)
	}
}

func TestRequestOriginUsesForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/auth/profile/oauth/feishu/callback", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "stella.example.com")

	if got := requestOrigin(req); got != "https://stella.example.com" {
		t.Fatalf("requestOrigin = %q, want https://stella.example.com", got)
	}
}
