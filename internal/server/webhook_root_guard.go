package server

import (
	"net/http"
	"strings"

	"github.com/CherryHQ/stella/internal/webhook"
)

// WebhookCapabilityReservation reserves the /webhooks/<capability> namespace
// ahead of access logging and OTel. Any request whose first /webhooks/ path
// segment carries the opaque capability prefix (webhook.TokenPrefix) receives an
// opaque 404 and never reaches next — regardless of method or suffix — so a
// disclosed capability cannot enter access logs or telemetry.
//
// It must wrap the root handler *outside* the instrumented admin chain and the
// legacy PAT ingress handler. Legacy PAT ingress (/webhooks/{channelID} where
// the id is not capability-shaped) passes through unchanged. A later phase
// deepens this reservation into real capability ingress rather than deleting it.
func WebhookCapabilityReservation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isReservedWebhookCapabilityPath(r.URL.Path) {
			// Bare 404: no body, no delegation, no logging. The capability never
			// crosses this boundary, so it cannot be captured downstream.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isReservedWebhookCapabilityPath reports whether path targets the reserved
// capability namespace: /webhooks/ followed by a first segment that starts with
// the opaque capability prefix. r.URL.Path is already percent-decoded by
// net/http, so escaped forms of the prefix resolve here too. Extra trailing
// segments and any HTTP method are covered because only the first segment is
// inspected and the caller applies this to every method.
func isReservedWebhookCapabilityPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/webhooks/")
	if !ok {
		return false
	}
	segment := rest
	if i := strings.IndexByte(segment, '/'); i >= 0 {
		segment = segment[:i]
	}
	return strings.HasPrefix(segment, webhook.TokenPrefix)
}
