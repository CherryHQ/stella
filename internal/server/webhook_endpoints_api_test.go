package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apitypes "github.com/CherryHQ/stella/api/types"
)

// TestWebhookEndpointLifecycleAPI drives the endpoint lifecycle through the real
// HTTP surface: Create discloses a one-time url, stable Get redacts it, Rotate
// with the observed etag issues a new url and invalidates the old etag, deleting
// the channel while active is a 409, and Revoke returns 204.
func TestWebhookEndpointLifecycleAPI(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "webhook-endpoint-agent")

	const channelID = "wh-endpoint-test"
	rr := doRequest(t, env, "POST", "/api/channels", map[string]any{
		"id":       channelID,
		"type":     "webhook",
		"agent_id": agentID,
		"enabled":  true,
		"config":   `{}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create channel status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}

	base := "/api/channels/" + channelID + "/webhook-endpoint"

	// Create discloses a one-time url and the safe metadata.
	rr = doRequest(t, env, "POST", base, map[string]any{"provider": "generic"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create endpoint status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	created := decodeEndpoint(t, rr.Body.Bytes())
	if created.Url == nil || !strings.Contains(*created.Url, "/webhooks/") {
		t.Fatalf("create did not disclose a one-time url: %+v", created)
	}
	if created.OwnerUserId != env.adminUser.ID || created.Provider != "generic" || created.Etag == "" {
		t.Fatalf("create metadata = %+v", created)
	}

	// Stable Get redacts the one-time url and echoes the same etag.
	rr = doRequest(t, env, "GET", base, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get endpoint status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	got := decodeEndpoint(t, rr.Body.Bytes())
	if got.Url != nil {
		t.Fatalf("stable Get leaked a url: %v", *got.Url)
	}
	if got.Etag != created.Etag {
		t.Fatalf("stable Get etag = %q, want %q", got.Etag, created.Etag)
	}

	// Rotate with the observed etag issues a new one-time url and a new etag.
	rr = doRequest(t, env, "POST", base+"/rotate", map[string]any{"etag": created.Etag})
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	rotated := decodeEndpoint(t, rr.Body.Bytes())
	if rotated.Url == nil || *rotated.Url == *created.Url {
		t.Fatalf("rotate did not issue a new url: %+v", rotated)
	}
	if rotated.Etag == created.Etag {
		t.Fatal("rotate did not change the etag")
	}

	// The stale (pre-rotation) etag is now a 409 conflict.
	rr = doRequest(t, env, "POST", base+"/rotate", map[string]any{"etag": created.Etag})
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale rotate status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}

	// Deleting the channel while the endpoint is active is a 409.
	rr = doRequest(t, env, "DELETE", "/api/channels/"+channelID, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("delete channel with active endpoint status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}

	// Revoke returns 204; a subsequent Get is 404.
	rr = doRequest(t, env, "DELETE", base, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, "GET", base, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get after revoke status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}

	// With the endpoint revoked, the channel deletes cleanly.
	rr = doRequest(t, env, "DELETE", "/api/channels/"+channelID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete channel after revoke status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestPersonalWebhookOwnership proves webhook channels and endpoint lifecycle are
// self-scoped for every account. Admin status grants no access to another
// owner's webhook, while non-webhook deployment channels remain admin-only.
func TestPersonalWebhookOwnership(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "personal-webhook-agent")
	owner, ownerToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-owner", "user")
	other, otherToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-other", "user")

	const channelID = "personal-webhook"
	create := func(token, channelType, id string) *httptest.ResponseRecorder {
		return doRequestWithSession(t, env.srv, token, "POST", "/api/channels", map[string]any{
			"id": id, "type": channelType, "agent_id": agentID, "config": `{}`,
		})
	}
	if rr := create(ownerToken, "webhook", channelID); rr.Code != http.StatusCreated {
		t.Fatalf("owner create webhook status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := create(otherToken, "telegram", "not-allowed"); rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin create deployment channel status=%d, want 403 body=%s", rr.Code, rr.Body.String())
	}

	channelPath := "/api/channels/" + channelID
	if rr := doRequestWithSession(t, env.srv, ownerToken, "PATCH", channelPath, map[string]any{
		"type": "telegram", "agent_id": agentID, "config": `{}`,
	}); rr.Code != http.StatusForbidden {
		t.Fatalf("personal webhook type conversion status=%d, want 403 body=%s", rr.Code, rr.Body.String())
	}
	endpointPath := channelPath + "/webhook-endpoint"
	for name, token := range map[string]string{"other user": otherToken, "admin": env.bearerToken} {
		t.Run(name+" cannot read channel", func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, token, "GET", channelPath, nil)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
			}
		})
		t.Run(name+" cannot update channel", func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, token, "PATCH", channelPath, map[string]any{
				"name": "stolen", "type": "webhook", "agent_id": agentID, "config": `{}`,
			})
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
			}
		})
		t.Run(name+" cannot activate endpoint", func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, token, "POST", endpointPath, map[string]any{"provider": "generic"})
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
			}
		})
	}

	// A spoofed owner field is not part of the contract and cannot alter the
	// trusted caller-derived owner.
	rr := doRequestWithSession(t, env.srv, ownerToken, "POST", endpointPath, map[string]any{
		"provider": "generic", "owner_user_id": other.ID,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner activate status=%d body=%s", rr.Code, rr.Body.String())
	}
	endpoint := decodeEndpoint(t, rr.Body.Bytes())
	if endpoint.OwnerUserId != owner.ID {
		t.Fatalf("endpoint owner=%q, want authenticated owner %q", endpoint.OwnerUserId, owner.ID)
	}

	for name, token := range map[string]string{"other user": otherToken, "admin": env.bearerToken} {
		t.Run(name+" cannot rotate", func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, token, "POST", endpointPath+"/rotate", map[string]any{"etag": endpoint.Etag})
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
			}
		})
		t.Run(name+" cannot revoke", func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, token, "DELETE", endpointPath, nil)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
			}
		})
		t.Run(name+" cannot delete channel", func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, token, "DELETE", channelPath, nil)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404 body=%s", rr.Code, rr.Body.String())
			}
		})
	}

	// Admins use the same personal path for their own webhooks.
	if rr := create(env.bearerToken, "webhook", "admin-personal-webhook"); rr.Code != http.StatusCreated {
		t.Fatalf("admin create own webhook status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func decodeEndpoint(t *testing.T, body []byte) apitypes.WebhookEndpoint {
	t.Helper()
	var ep apitypes.WebhookEndpoint
	if err := json.Unmarshal(body, &ep); err != nil {
		t.Fatalf("decode endpoint: %v (body: %s)", err, string(body))
	}
	return ep
}
