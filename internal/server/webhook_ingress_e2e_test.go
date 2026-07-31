package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/server"
)

// webhookGuardHandler wraps the server exactly as the composition root does: the
// capability reservation owns /webhooks/ ahead of the admin chain.
func webhookGuardHandler(env *testEnv) http.Handler {
	return server.WebhookCapabilityReservation(env.srv.WebhookIngressHandler(), env.srv.Handler())
}

func postWebhook(t *testing.T, h http.Handler, capability string) int {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/webhooks/"+capability, strings.NewReader("trigger")))
	return rr.Code
}

func createAgentWithScope(t *testing.T, env *testEnv, name, scope string) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, env.bearerToken, "POST", "/api/agents", config.Agent{
		Name: name, Model: "anthropic/claude-sonnet-4-6", Enabled: true, Scope: scope,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent %s: status=%d (body: %s)", name, rr.Code, rr.Body.String())
	}
	var created config.Agent
	if err := json.Unmarshal(parseResponse(t, rr).Data, &created); err != nil {
		t.Fatalf("unmarshal agent: %v", err)
	}
	return created.ID
}

func createWebhookCapability(t *testing.T, env *testEnv, token, agentID string) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, token, "POST", "/api/webhooks", map[string]any{"name": "e2e", "agent_id": agentID, "provider": "generic"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create webhook: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var item apitypes.Webhook
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal webhook: %v", err)
	}
	if item.Url == nil {
		t.Fatal("create did not disclose a url")
	}
	idx := strings.LastIndex(*item.Url, "/webhooks/")
	if idx < 0 {
		t.Fatalf("url has no /webhooks/ segment: %q", *item.Url)
	}
	return (*item.Url)[idx+len("/webhooks/"):]
}

func setUserAgents(t *testing.T, env *testEnv, userID string, agentIDs []string) {
	t.Helper()
	rr := doRequest(t, env, "PATCH", "/api/users/"+userID+"/agents", map[string]any{"agent_ids": agentIDs})
	if rr.Code != http.StatusOK {
		t.Fatalf("set user agents: status=%d (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestWebhookIngressCapabilityReplacesPAT proves the generic capability ingress:
// the old channel-ID/PAT route is an opaque 404, a fixed-owner capability is
// admitted (never 404), a withdrawn restricted-Agent assignment makes later
// admission fail closed with 404 while restoring it permits the same endpoint,
// and a system-scope Agent needs no assignment.
func TestWebhookIngressCapabilityReplacesPAT(t *testing.T) {
	env := setupAdmin(t)
	guard := webhookGuardHandler(env)

	owner, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "wh-owner", auth.RoleUser)

	// A restricted agent the owner can use only while assigned.
	restrictedAgent := createAgentWithScope(t, env, "restricted-wh", config.AgentScopeRestricted)
	setUserAgents(t, env, owner.ID, []string{restrictedAgent})
	restrictedCap := createWebhookCapability(t, env, ownerToken, restrictedAgent)

	// A system agent every user may execute without assignment.
	systemAgent := createAgentWithScope(t, env, "system-wh", config.AgentScopeSystem)
	systemCap := createWebhookCapability(t, env, ownerToken, systemAgent)

	// Old channel-ID / PAT-style ids are opaque 404s: they are not capabilities.
	if code := postWebhook(t, guard, "wh-restricted"); code != http.StatusNotFound {
		t.Fatalf("old channel-id route = %d, want 404", code)
	}
	if code := postWebhook(t, guard, "stella_pat_deadbeef"); code != http.StatusNotFound {
		t.Fatalf("PAT-style id = %d, want 404", code)
	}

	// The fixed-owner capability is admitted (authorized): never 404. It may be
	// 202/200 (admitted) or 503 (no live agent runtime in this harness), but the
	// point is the capability is accepted and identity is fixed by the endpoint.
	if code := postWebhook(t, guard, restrictedCap); code == http.StatusNotFound {
		t.Fatalf("assigned restricted capability = 404, want admitted (non-404)")
	}

	// Withdraw the assignment: later admission fails closed with an opaque 404.
	setUserAgents(t, env, owner.ID, []string{})
	if code := postWebhook(t, guard, restrictedCap); code != http.StatusNotFound {
		t.Fatalf("withdrawn restricted capability = %d, want 404", code)
	}

	// Restoring the assignment permits the same endpoint again.
	setUserAgents(t, env, owner.ID, []string{restrictedAgent})
	if code := postWebhook(t, guard, restrictedCap); code == http.StatusNotFound {
		t.Fatalf("restored restricted capability = 404, want admitted (non-404)")
	}

	// System-agent semantics are unchanged: no assignment is needed.
	if code := postWebhook(t, guard, systemCap); code == http.StatusNotFound {
		t.Fatalf("system capability = 404, want admitted (non-404)")
	}
}
