package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/webhook"
)

// instrumentedStandIn is the access-log middleware wrapping a terminal handler.
// In production the reservation wraps the OTel + access-log + legacy PAT chain;
// this stand-in captures the same "did the capability reach instrumentation?"
// signal a unit test can assert on. If next is never called, neither access
// logging nor OTel (both downstream of the reservation) can observe the request.
func instrumentedStandIn(t *testing.T) (handler http.Handler, called *bool, log *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	reached := false
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	return WebhookCapabilityReservation(s.accessLogMiddleware(terminal)), &reached, &buf
}

func TestWebhookCapabilityReservationIsInertAndUnlogged(t *testing.T) {
	minted, err := credential.MintOpaqueWithPrefix(webhook.TokenPrefix)
	if err != nil {
		t.Fatalf("mint capability: %v", err)
	}
	capability := minted.Plaintext // the exact string the Create/Rotate `url` discloses

	guarded, reached, logBuf := instrumentedStandIn(t)

	cases := []struct{ name, method, path string }{
		{"canonical POST", http.MethodPost, "/webhooks/" + capability},
		{"wrong method GET", http.MethodGet, "/webhooks/" + capability},
		{"wrong method PUT", http.MethodPut, "/webhooks/" + capability},
		{"extra suffix", http.MethodPost, "/webhooks/" + capability + "/extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*reached = false
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if *reached {
				t.Fatal("capability request reached the instrumented chain")
			}
		})
	}

	// The disclosed capability (and its non-secret public id) never entered the
	// access log because the reservation short-circuited before instrumentation.
	if out := logBuf.String(); strings.Contains(out, capability) || strings.Contains(out, minted.PublicID) {
		t.Fatalf("capability leaked into access log: %q", out)
	}
}

func TestWebhookCapabilityReservationLegacyPATPassesThrough(t *testing.T) {
	guarded, reached, logBuf := instrumentedStandIn(t)

	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhooks/my-channel", nil))

	if !*reached {
		t.Fatal("legacy PAT ingress did not reach its handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy status = %d, want 200", rec.Code)
	}
	if !strings.Contains(logBuf.String(), "/webhooks/my-channel") {
		t.Fatalf("legacy path was not access-logged: %q", logBuf.String())
	}
}

func TestIsReservedWebhookCapabilityPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/webhooks/" + webhook.TokenPrefix + "abc123", true},
		{"/webhooks/" + webhook.TokenPrefix + "abc/extra", true},
		{"/webhooks/my-channel", false},
		{"/webhooks/", false},
		{"/api/channels", false},
		{"/webhooksstella_whk_x", false}, // not under the /webhooks/ prefix
	}
	for _, tc := range cases {
		if got := isReservedWebhookCapabilityPath(tc.path); got != tc.want {
			t.Errorf("isReservedWebhookCapabilityPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
