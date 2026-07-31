package server_test

import (
	"encoding/json"
	"net/http"
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
	rr = doRequest(t, env, "POST", base, map[string]any{
		"owner_user_id": env.adminUser.ID,
		"provider":      "generic",
	})
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

func decodeEndpoint(t *testing.T, body []byte) apitypes.WebhookEndpoint {
	t.Helper()
	var ep apitypes.WebhookEndpoint
	if err := json.Unmarshal(body, &ep); err != nil {
		t.Fatalf("decode endpoint: %v (body: %s)", err, string(body))
	}
	return ep
}
