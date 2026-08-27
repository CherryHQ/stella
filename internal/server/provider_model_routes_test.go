package server_test

import (
	"net/http"
	"testing"
)

// TestProviderModelRefreshRouting pins which verb belongs to which provider
// path. Refreshing upstream models is a POST on /models; /evidence is a
// read-only projection and must never gain that side effect.
func TestProviderModelRefreshRouting(t *testing.T) {
	env := setupAdmin(t)
	providerID := "route-shape"
	if rr := doRequest(t, env, http.MethodPost, "/api/providers", map[string]any{
		"id": providerID, "type": "openai", "name": "Route shape", "enabled": true, "api_key": "sk-test", "base_url": "https://gateway.example.test/v1",
		"models": map[string]any{"gpt-test": map[string]any{"enabled": true, "cost": map[string]any{"input": 1.25, "output": 2.5}}},
	}); rr.Code != http.StatusCreated {
		t.Fatalf("create provider: status=%d body=%s", rr.Code, rr.Body.String())
	}

	// The upstream call cannot succeed against a test gateway; routing is what
	// this asserts, so only "the route exists" is checked.
	if rr := doRequest(t, env, http.MethodPost, "/api/providers/"+providerID+"/models", map[string]any{}); rr.Code == http.StatusNotFound || rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST /models is not routed: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/providers/"+providerID+"/evidence", map[string]any{}); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /evidence: status=%d want 405 body=%s", rr.Code, rr.Body.String())
	}
}
