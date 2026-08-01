package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
)

func decodeWebhook(t *testing.T, rrBody []byte) apitypes.Webhook {
	t.Helper()
	var item apitypes.Webhook
	if err := json.Unmarshal(rrBody, &item); err != nil {
		t.Fatalf("decode webhook: %v (%s)", err, rrBody)
	}
	return item
}

func createPersonalWebhook(t *testing.T, env *testEnv, token, agentID string) apitypes.Webhook {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/webhooks", map[string]any{"name": "deploy", "agent_id": agentID})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create webhook: %d %s", rr.Code, rr.Body.String())
	}
	return decodeWebhook(t, rr.Body.Bytes())
}

func TestWebhookLifecycleAPISecretDisclosureAndImmutableProvider(t *testing.T) {
	env := setupAdmin(t)
	owner, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-owner", auth.RoleUser)
	agentID := createAgentAsUser(t, env, ownerToken, "webhook-agent")
	created := createPersonalWebhook(t, env, ownerToken, agentID)
	if created.Provider != "generic" {
		t.Fatalf("default provider = %q, want generic", created.Provider)
	}
	invalidProvider := doRequestWithSession(t, env.srv, ownerToken, http.MethodPost, "/api/webhooks", map[string]any{"name": "invalid", "agent_id": agentID, "provider": "github"})
	if invalidProvider.Code != http.StatusBadRequest {
		t.Fatalf("unsupported provider = %d, want 400 (%s)", invalidProvider.Code, invalidProvider.Body.String())
	}
	if created.Url == nil || !strings.Contains(*created.Url, "/webhooks/") {
		t.Fatalf("Create did not disclose URL: %+v", created)
	}
	if created.UserId == nil || created.UserId.String() != owner.ID {
		t.Fatalf("owner=%v want %s", created.UserId, owner.ID)
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/agents/"+agentID, nil); rr.Code != http.StatusConflict {
		t.Fatalf("delete bound Agent = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}
	rr := doRequestWithSession(t, env.srv, ownerToken, http.MethodGet, "/api/webhooks/"+created.Id.String(), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get=%d %s", rr.Code, rr.Body.String())
	}
	stable := decodeWebhook(t, rr.Body.Bytes())
	if stable.Url != nil {
		t.Fatal("stable GET disclosed one-time URL")
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodGet, "/api/webhooks", nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), *created.Url) {
		t.Fatalf("stable List leaked one-time URL: %d %s", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodPatch, "/api/webhooks/"+created.Id.String(), map[string]any{"name": "renamed"})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch=%d %s", rr.Code, rr.Body.String())
	}
	updated := decodeWebhook(t, rr.Body.Bytes())
	if updated.Name != "renamed" || updated.Provider != "generic" {
		t.Fatalf("patch result=%+v", updated)
	}
	for _, body := range []map[string]any{{"provider": "generic"}, {"name": nil}, {"agent_id": nil}, {"is_enabled": nil}, {"wait_timeout_seconds": nil}, {"max_run_timeout_seconds": nil}} {
		rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodPatch, "/api/webhooks/"+created.Id.String(), body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid patch %#v = %d %s", body, rr.Code, rr.Body.String())
		}
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodPost, "/api/webhooks/"+created.Id.String()+"/rotate", map[string]any{"etag": updated.Etag})
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate=%d %s", rr.Code, rr.Body.String())
	}
	rotated := decodeWebhook(t, rr.Body.Bytes())
	if rotated.Url == nil || *rotated.Url == *created.Url || rotated.Etag == updated.Etag {
		t.Fatalf("rotation=%+v", rotated)
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodPost, "/api/webhooks/"+created.Id.String()+"/rotate", map[string]any{"etag": updated.Etag})
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale rotate=%d", rr.Code)
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodDelete, "/api/webhooks/"+created.Id.String(), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodGet, "/api/webhooks/"+created.Id.String(), nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get deleted=%d", rr.Code)
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/agents/"+agentID, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete unbound Agent = %d, want 204 (%s)", rr.Code, rr.Body.String())
	}
}

func TestWebhookListPaginationAndServerGeneratedID(t *testing.T) {
	env := setupAdmin(t)
	_, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-pages", auth.RoleUser)
	agentID := createAgentAsUser(t, env, token, "webhook-page-agent")

	// Output-only identity input is ignored: the server mints the UUID and derives
	// user_id from authentication, so no caller can re-enter the channel namespace
	// or spoof ownership.
	spoofedUserID := uuid.NewString()
	rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/webhooks", map[string]any{
		"id": "weixin", "user_id": spoofedUserID, "name": "server-owned-id", "agent_id": agentID, "provider": "generic",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create with output-only fields = %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	serverOwned := decodeWebhook(t, rr.Body.Bytes())
	if serverOwned.Id.String() == "weixin" || serverOwned.UserId.String() == spoofedUserID {
		t.Fatalf("server accepted caller identity: %+v", serverOwned)
	}
	for i := range 2 {
		rr = doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/webhooks", map[string]any{
			"name": "hook-" + string(rune('a'+i)), "agent_id": agentID, "provider": "generic",
		})
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %d = %d (%s)", i, rr.Code, rr.Body.String())
		}
	}
	rr = doRequestWithSession(t, env.srv, token, http.MethodGet, "/api/webhooks?page_size=2", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("page 1 = %d (%s)", rr.Code, rr.Body.String())
	}
	var first apitypes.WebhookList
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Webhooks) != 2 || first.NextPageToken == nil {
		t.Fatalf("page 1 = %+v, want two rows and token", first)
	}
	rr = doRequestWithSession(t, env.srv, token, http.MethodGet, "/api/webhooks?page_size=2&page_token="+url.QueryEscape(*first.NextPageToken), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("page 2 = %d (%s)", rr.Code, rr.Body.String())
	}
	var second apitypes.WebhookList
	if err := json.Unmarshal(rr.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Webhooks) != 1 || second.NextPageToken != nil {
		t.Fatalf("page 2 = %+v, want final row", second)
	}
}

func TestWebhookBindingErrorsUseStructuredContract(t *testing.T) {
	env := setupAdmin(t)
	for _, path := range []string{"/api/webhooks/not-a-uuid", "/api/webhooks?page_size=not-a-number"} {
		rr := doRequest(t, env, http.MethodGet, path, nil)
		if rr.Code != http.StatusBadRequest || rr.Header().Get("Content-Type") != "application/json" || !strings.Contains(rr.Body.String(), `"status":"INVALID_ARGUMENT"`) {
			t.Fatalf("GET %s = %d %q %s", path, rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
		}
	}
}

func TestWebhookAPIAuthAndOwnerIsolation(t *testing.T) {
	env := setupAdmin(t)
	owner, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-owner", auth.RoleUser)
	_, otherToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-other", auth.RoleUser)
	agentID := createAgentAsUser(t, env, ownerToken, "owner-agent")
	created := createPersonalWebhook(t, env, ownerToken, agentID)
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		rr := doRequestWithSession(t, env.srv, "", method, "/api/webhooks/"+created.Id.String(), map[string]any{"name": "x"})
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unauth %s=%d", method, rr.Code)
		}
	}
	for label, token := range map[string]string{"other": otherToken, "admin": env.bearerToken} {
		for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
			rr := doRequestWithSession(t, env.srv, token, method, "/api/webhooks/"+created.Id.String(), map[string]any{"name": "stolen"})
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s %s=%d %s", label, method, rr.Code, rr.Body.String())
			}
		}
		rr := doRequestWithSession(t, env.srv, token, http.MethodPost, "/api/webhooks/"+created.Id.String()+"/rotate", map[string]any{"etag": created.Etag})
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s rotate=%d", label, rr.Code)
		}
	}
	// Malformed agent input is a 400; a syntactically valid inaccessible Agent is a policy denial.
	rr := doRequestWithSession(t, env.srv, ownerToken, http.MethodPost, "/api/webhooks", map[string]any{"name": "bad", "agent_id": "", "provider": "generic"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed agent=%d %s", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodPost, "/api/webhooks", map[string]any{"name": "denied", "agent_id": "missing-agent", "provider": "generic"})
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "webhook:") {
		t.Fatalf("inaccessible agent=%d %s", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, ownerToken, http.MethodPatch, "/api/webhooks/"+created.Id.String(), map[string]any{"agent_id": "missing-agent"})
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "webhook:") {
		t.Fatalf("inaccessible patch agent=%d %s", rr.Code, rr.Body.String())
	}
	_ = owner
}
