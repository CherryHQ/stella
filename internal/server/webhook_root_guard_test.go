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

// guardHarness wires the reservation exactly as the composition root does: the
// ingress handler is wrapped in access logging (standing in for the full
// instrumented chain), and a separate recorder stands in for the admin chain.
type guardHarness struct {
	handler      http.Handler
	log          *bytes.Buffer
	ingressReq   *http.Request
	ingressHit   bool
	nextHit      bool
	capability   string
	capabilityOK bool
	wait         bool
	waitOK       bool
}

func newGuardHarness(t *testing.T) *guardHarness {
	t.Helper()
	h := &guardHarness{log: &bytes.Buffer{}}
	s := &Server{log: slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	ingress := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ingressHit = true
		h.ingressReq = r
		h.capability, h.capabilityOK = webhookCapabilityFromContext(r.Context())
		h.wait, h.waitOK = webhookWaitFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.nextHit = true
		w.WriteHeader(http.StatusOK)
	})
	h.handler = WebhookCapabilityReservation(s.accessLogMiddleware(ingress), next)
	return h
}

func (h *guardHarness) serve(method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader("body")))
	return rec
}

func TestReservationDispatchesCanonicalPostSanitized(t *testing.T) {
	minted, err := credential.MintOpaqueWithPrefix(webhook.TokenPrefix)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	cap := minted.Plaintext
	h := newGuardHarness(t)
	rec := h.serve(http.MethodPost, "/webhooks/"+cap+"?wait=true&trace=secretquery")

	if rec.Code != http.StatusOK || !h.ingressHit || h.nextHit {
		t.Fatalf("dispatch: code=%d ingress=%v next=%v", rec.Code, h.ingressHit, h.nextHit)
	}
	// The capability is delivered only via private context.
	if !h.capabilityOK || h.capability != cap {
		t.Fatalf("capability context = %q (%v), want %q", h.capability, h.capabilityOK, cap)
	}
	if !h.waitOK || h.wait != true {
		t.Fatalf("wait context = %v (%v), want true", h.wait, h.waitOK)
	}
	// Every URL-bearing field the instrumented chain saw is sanitized.
	if h.ingressReq.URL.Path != sanitizedWebhookPath || h.ingressReq.URL.RawQuery != "" || h.ingressReq.URL.RawPath != "" || h.ingressReq.RequestURI != sanitizedWebhookPath {
		t.Fatalf("unsanitized request: path=%q rawpath=%q rawquery=%q uri=%q", h.ingressReq.URL.Path, h.ingressReq.URL.RawPath, h.ingressReq.URL.RawQuery, h.ingressReq.RequestURI)
	}
	// Neither the capability nor the raw query text reached the access log.
	if out := h.log.String(); strings.Contains(out, cap) || strings.Contains(out, minted.PublicID) || strings.Contains(out, "secretquery") {
		t.Fatalf("capability/query leaked into access log: %q", out)
	}
}

func TestReservationRedactionMatrix(t *testing.T) {
	minted, _ := credential.MintOpaqueWithPrefix(webhook.TokenPrefix)
	cap := minted.Plaintext
	cases := []struct {
		name        string
		method      string
		target      string
		wantCode    int
		wantIngress bool
	}{
		{"canonical", http.MethodPost, "/webhooks/" + cap, http.StatusOK, true},
		{"unknown-capability", http.MethodPost, "/webhooks/some-old-channel-id", http.StatusOK, true},
		{"escaped", http.MethodPost, "/webhooks/%73" + cap[1:], http.StatusOK, true},
		{"query", http.MethodPost, "/webhooks/" + cap + "?wait=false&x=leak", http.StatusOK, true},
		{"wrong-method-GET", http.MethodGet, "/webhooks/" + cap, http.StatusNotFound, false},
		{"wrong-method-HEAD", http.MethodHead, "/webhooks/" + cap, http.StatusNotFound, false},
		{"wrong-method-PUT", http.MethodPut, "/webhooks/" + cap, http.StatusNotFound, false},
		{"extra-segments", http.MethodPost, "/webhooks/" + cap + "/extra", http.StatusNotFound, false},
		{"empty-suffix", http.MethodPost, "/webhooks/", http.StatusNotFound, false},
		// Prefix-equivalent malformed paths carrying a capability: opaque 404
		// before the admin chain, never normalized-then-dispatched.
		{"double-slash", http.MethodPost, "//webhooks/" + cap, http.StatusNotFound, false},
		{"case-variant", http.MethodPost, "/Webhooks/" + cap, http.StatusNotFound, false},
		{"dot-segment-outside", http.MethodPost, "/x/../webhooks/" + cap, http.StatusNotFound, false},
		{"dot-segment-inside", http.MethodPost, "/webhooks/../webhooks/" + cap, http.StatusNotFound, false},
		{"encoded-prefix", http.MethodPost, "/%77ebhooks/" + cap, http.StatusNotFound, false},
		{"double-encoded-prefix", http.MethodPost, "/%2577ebhooks/" + cap, http.StatusNotFound, false},
		{"double-encoded-token", http.MethodPost, "//x/stella_whk%255f" + cap[len(webhook.TokenPrefix):], http.StatusNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuardHarness(t)
			rec := h.serve(tc.method, tc.target)
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if h.ingressHit != tc.wantIngress {
				t.Fatalf("ingressHit = %v, want %v", h.ingressHit, tc.wantIngress)
			}
			if h.nextHit {
				t.Fatal("a /webhooks/ request must never reach the admin chain")
			}
			// No original capability or query text ever reaches the access log.
			if out := h.log.String(); strings.Contains(out, cap) || strings.Contains(out, "leak") {
				t.Fatalf("%s leaked into access log: %q", tc.name, out)
			}
		})
	}
}

func TestReservationInvalidWaitIs400BeforeIngress(t *testing.T) {
	minted, _ := credential.MintOpaqueWithPrefix(webhook.TokenPrefix)
	h := newGuardHarness(t)
	rec := h.serve(http.MethodPost, "/webhooks/"+minted.Plaintext+"?wait=maybe")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if h.ingressHit {
		t.Fatal("invalid wait must be rejected before ingress admission")
	}
}

func TestReservationPassesThroughNonWebhookPaths(t *testing.T) {
	h := newGuardHarness(t)
	rec := h.serve(http.MethodGet, "/api/channels")
	if rec.Code != http.StatusOK || !h.nextHit || h.ingressHit {
		t.Fatalf("passthrough: code=%d next=%v ingress=%v", rec.Code, h.nextHit, h.ingressHit)
	}
}
