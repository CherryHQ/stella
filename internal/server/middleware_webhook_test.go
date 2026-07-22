package server

import (
	"net/http"
	"testing"
)

func TestWebhookCapabilityIngressIsPostOnlyAuthExempt(t *testing.T) {
	if !isAuthExempt(http.MethodPost, "/webhooks/stella_whk_capability") {
		t.Fatal("POST capability ingress must bypass session authentication")
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if isAuthExempt(method, "/webhooks/stella_whk_capability") {
			t.Errorf("%s webhook path must not be auth-exempt", method)
		}
	}
}
