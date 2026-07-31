package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/CherryHQ/stella/internal/webhook"
)

// sanitizedWebhookPath is the fixed path the capability request carries once the
// reservation has moved the real capability into private context. No capability
// or query text is ever present on the URL the instrumented chain observes.
const sanitizedWebhookPath = "/webhooks/"

type webhookCapabilityCtxKey struct{}

type webhookWaitCtxKey struct{}

// webhookCapabilityFromContext returns the canonical single-segment capability
// the reservation parsed from the path, if this request came through it.
func webhookCapabilityFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(webhookCapabilityCtxKey{}).(string)
	return v, ok
}

// webhookWaitFromContext returns the typed wait override the reservation parsed
// from the query, if the caller supplied wait=true|false.
func webhookWaitFromContext(ctx context.Context) (bool, bool) {
	v, ok := ctx.Value(webhookWaitCtxKey{}).(bool)
	return v, ok
}

// capabilityScanRounds bounds the repeated percent-unescape used to detect a
// capability token hidden behind single or double encoding. Three rounds cover
// the double-encoded forms the redaction matrix exercises without unbounded work.
const capabilityScanRounds = 3

// WebhookCapabilityReservation owns the entire /webhooks/ namespace ahead of
// access logging and OTel. It is the single, deepened evolution of the Phase-1
// reservation: nothing else guards this prefix.
//
//   - A canonical POST /webhooks/<single-segment> — where the request path
//     *literally* begins with "/webhooks/" (not after decoding, case-folding,
//     slash-collapsing, or dot-segment resolution) — is admitted inward: the
//     capability and an optional typed wait override move into private context,
//     every URL-bearing field is sanitized so the raw capability and query never
//     reach the instrumented chain, and the request is dispatched to ingress.
//   - Any other method, an empty or multi-segment suffix, gets an opaque 404
//     without touching ingress, access logging, or OTel.
//   - An invalid wait value is a 400 before any admission work; the raw query is
//     never forwarded.
//   - A request that is NOT the literal canonical prefix but still carries a
//     capability token anywhere in its path/raw-path/request-target (a
//     prefix-equivalent malformed path: //webhooks/…, /Webhooks/…, dot-segment
//     forms, percent/double-percent-encoded prefixes) gets an opaque 404 before
//     the admin chain, so a capability can never reach admin telemetry. Malformed
//     requests are never normalized-then-dispatched.
//   - Every other request passes through unchanged to next (the admin chain).
//
// A later phase deepens ingress behind this seam rather than replacing the guard.
func WebhookCapabilityReservation(ingress, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Canonical dispatch requires the literal "/webhooks/" prefix in both the
		// decoded and the encoding-preserving path. A prefix that only becomes
		// "/webhooks/" after decoding/normalization is handled as malformed below.
		if !strings.HasPrefix(r.URL.EscapedPath(), "/webhooks/") || !strings.HasPrefix(r.URL.Path, "/webhooks/") {
			// Not the literal capability namespace. If the request nonetheless
			// smuggles a capability token (prefix-equivalent malformed path), return
			// an opaque 404 before the admin chain so it never enters telemetry.
			if requestMentionsCapability(r) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// From here the reservation owns the request; nothing falls through to the
		// admin chain, so a capability can never enter the admin middleware.
		rest := strings.TrimPrefix(r.URL.Path, "/webhooks/")
		if r.Method != http.MethodPost || rest == "" || strings.Contains(rest, "/") {
			// Wrong method, empty, or multi-segment: opaque 404, uninstrumented.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		capability := rest

		waitOverride, hasWait, ok := parseWaitOverride(r.URL.RawQuery)
		if !ok {
			// Invalid wait is rejected before any admission; no capability or raw
			// query is echoed.
			http.Error(w, "invalid wait parameter: use true or false", http.StatusBadRequest)
			return
		}

		ctx := context.WithValue(r.Context(), webhookCapabilityCtxKey{}, capability)
		if hasWait {
			ctx = context.WithValue(ctx, webhookWaitCtxKey{}, waitOverride)
		}
		r2 := r.Clone(ctx)
		// Sanitize every URL-bearing field before the instrumented chain sees it.
		r2.URL.Path = sanitizedWebhookPath
		r2.URL.RawPath = ""
		r2.URL.RawQuery = ""
		r2.RequestURI = sanitizedWebhookPath
		ingress.ServeHTTP(w, r2)
	})
}

// requestMentionsCapability reports whether a non-canonical request still carries
// a capability token in any URL-bearing field, including single/double
// percent-encoded forms. It exists so a prefix-equivalent malformed path cannot
// leak the capability into admin telemetry by falling through to next.
func requestMentionsCapability(r *http.Request) bool {
	return containsCapabilityToken(r.URL.Path) ||
		containsCapabilityToken(r.URL.RawPath) ||
		containsCapabilityToken(r.RequestURI)
}

// containsCapabilityToken reports whether s (or any bounded repeated unescape of
// it) contains the opaque capability prefix.
func containsCapabilityToken(s string) bool {
	cur := s
	for range capabilityScanRounds + 1 {
		if cur == "" {
			return false
		}
		if strings.Contains(cur, webhook.TokenPrefix) {
			return true
		}
		dec, err := url.PathUnescape(cur)
		if err != nil || dec == cur {
			return false
		}
		cur = dec
	}
	return false
}

// parseWaitOverride reads an optional wait=true|false from raw query text without
// exposing the rest of the query. It returns (value, present, valid).
func parseWaitOverride(rawQuery string) (value bool, present bool, valid bool) {
	if rawQuery == "" {
		return false, false, true
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false, false, false
	}
	raw := q.Get("wait")
	if raw == "" {
		return false, false, true
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false, false
	}
	return v, true, true
}
